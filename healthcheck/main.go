package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	xcp "github.com/tetrateio/xcp/api/core/v2"
)

type EndpointStatus struct {
	Healthy       bool
	Retries       int
	LastCheckTime time.Time
}

var (
	statusCache           map[string]EndpointStatus
	hostBasedStatusCache  map[string]map[string]EndpointStatus
	localHostHealth       map[string]bool
	cacheMutex            sync.RWMutex
	hostCacheMutex        sync.RWMutex
	localHostCacheMutex   sync.RWMutex
	endpointsMutex        sync.RWMutex
	defaultRetries        = 3
	defaultInterval       = 2 * time.Second
	endpoints             map[string][]string
	tier1GatewaySelector  string
	tier1GatewayNamespace string
	xcpEdgeDebugEndpoint  string

	// endpoints            = map[string][]string{
	// 	"eastus":    {"10.160.1.75"},
	// 	"centralus": {"10.158.0.223"},
	// }
	regexAfter  = regexp.MustCompile("(.+(\\|\\|))")
	regexBefore = regexp.MustCompile("::(.*)")
	regionRegex = map[string]*regexp.Regexp{}
	allRegions  = []string{}
)

func main() {
	localRegion := os.Getenv("REGION")
	retriesEnv := os.Getenv("RETRIES")
	intervalEnv := os.Getenv("HEALTH_CHECK_INTERVAL")
	xcpEdgeDebugEndpoint = os.Getenv("XCP_EDGE_DEBUG_ENDPOINT")
	tier1GatewaySelector = os.Getenv("TIER1_GATEWAY_SELECTOR")
	tier1GatewayNamespace = os.Getenv("TIER1_GATEWAY_NAMESPACE")

	// Parse environment variables or use default values
	retries := defaultRetries
	if retriesEnv != "" {
		parsedRetries, err := strconv.Atoi(retriesEnv)
		if err == nil {
			retries = parsedRetries
		} else {
			log.Printf("Invalid value for RETRIES: %s. Using default value.", retriesEnv)
		}
	}

	interval := defaultInterval
	if intervalEnv != "" {
		intervalDuration, err := time.ParseDuration(intervalEnv)
		if err == nil {
			interval = intervalDuration
		}
	}

	// Calculate the window for the cached status based on the number of retries and interval
	cachedStatusWindow := time.Duration(retries) * interval

	// Initialize the cache
	statusCache = make(map[string]EndpointStatus)
	hostBasedStatusCache = make(map[string]map[string]EndpointStatus)
	localHostHealth = make(map[string]bool)

	// Start a goroutine to populate tier1 endpoints
	go populateTier1Endpoints()

	// Start a goroutine to perform asynchronous health checks
	go performHealthChecks(localRegion, retries, interval)

	// Start a goroutine to populate host based status
	go populateHostBasedStatus(localRegion, retries, interval)

	http.HandleFunc("/health/", func(w http.ResponseWriter, r *http.Request) {
		regions := strings.Split(strings.TrimPrefix(r.URL.Path, "/health/"), "/")

		log.Println("Requested Regions: ", regions)

		var isLocalRegionPresent bool
		priorityRegions := []string{}
		regionsTobeChecked := regions
		// Check if the requested region matches the local region
		for _, region := range regions {
			if region == localRegion {
				isLocalRegionPresent = true
				log.Println("local region is present")
				break
			}
			// Check if any endpoint in the priority regions (regions before local region) is healthy
			priorityRegions = append(priorityRegions, region)
		}

		if isLocalRegionPresent {
			regionsTobeChecked = priorityRegions
		} else {
			log.Println("local region is not present")
		}

		// /east/central/west
		// if local is central -> [east]
		// if local is eastasia -> [east, central, west]
		log.Println("regions to be checked before checking the local endpoints ", regionsTobeChecked)
		// Check if any endpoint in the requested regions is healthy
		anyHealthy := false
		cacheMutex.RLock()
		for _, region := range regionsTobeChecked {
			endpointsMutex.RLock()
			for _, endpoint := range endpoints[region] {
				status, ok := statusCache[endpoint]
				if ok && status.Healthy && time.Since(status.LastCheckTime) < cachedStatusWindow {
					log.Printf("endpoint %s found healthy in region %s", endpoint, region)
					anyHealthy = true
					break
				}
			}
			endpointsMutex.RUnlock()
		}
		cacheMutex.RUnlock()

		if anyHealthy {
			// If at least one endpoint found in the requested regions is healthy, return Bad Gateway
			log.Println("send unhealthy as healthy endpoint found in requested regions")
			w.WriteHeader(http.StatusBadGateway)
			return
		}

		// Return the local endpoint status if none of the specified region endpoints are healthy
		cacheMutex.RLock()
		localEndpoints := endpoints[localRegion]
		anyHealthy = false
		for _, endpoint := range localEndpoints {
			status, ok := statusCache[endpoint]
			if ok && status.Healthy && time.Since(status.LastCheckTime) < cachedStatusWindow {
				anyHealthy = true
				break
			}
		}
		cacheMutex.RUnlock()

		if anyHealthy {
			// If at least one local endpoint is healthy, return OK
			log.Println("send local healthy")
			w.WriteHeader(http.StatusOK)
		} else {
			// If none of the local endpoints are healthy, return Bad Gateway
			log.Println("send unhealthy")
			w.WriteHeader(http.StatusBadGateway)
		}
	})

	http.HandleFunc("/localhealth/", func(w http.ResponseWriter, r *http.Request) {
		localHostCacheMutex.RLock()
		defer localHostCacheMutex.RUnlock()
		b, err := json.Marshal(localHostHealth)
		fmt.Println("localHostHealth", localHostHealth)
		if err != nil {
			log.Println("error in marshalling healthy endpoints")
		}
		w.Write(b)
	})

	// /endpoints/eastus
	http.HandleFunc("/endpoints/", func(w http.ResponseWriter, r *http.Request) {
		regions := strings.Split(strings.TrimPrefix(r.URL.Path, "/endpoints/"), "/")
		regionsDone := make(map[string]bool)
		// Check if any endpoint in the requested regions is healthy
		healthyEndpoints := map[string][]string{}
		cacheMutex.RLock()
		for host, status := range hostBasedStatusCache {
			anyHealthy := false
			for _, region := range regions {
				regionsDone[region] = true
				endpointsMutex.RLock()
				for _, endpoint := range endpoints[region] {
					if status[endpoint].Healthy && time.Since(status[endpoint].LastCheckTime) < cachedStatusWindow {
						log.Printf("endpoint %s found healthy in region %s", endpoint, region)
						anyHealthy = true
						if _, ok := healthyEndpoints[host]; !ok {
							healthyEndpoints[host] = []string{}
						}
						healthyEndpoints[host] = append(healthyEndpoints[host], endpoint)
					}
				}
				endpointsMutex.RUnlock()

				if anyHealthy {
					break
				}
			}

			if !anyHealthy {
				endpointsMutex.RLock()
				for _, region := range allRegions {
					if regionsDone[region] {
						continue
					}
					for _, endpoint := range endpoints[region] {
						if status[endpoint].Healthy && time.Since(status[endpoint].LastCheckTime) < cachedStatusWindow {
							log.Printf("endpoint %s found healthy in region %s", endpoint, region)
							if _, ok := healthyEndpoints[host]; !ok {
								healthyEndpoints[host] = []string{}
							}
							healthyEndpoints[host] = append(healthyEndpoints[host], endpoint)
						}
					}
					endpointsMutex.RUnlock()
				}
			}
		}
		cacheMutex.RUnlock()

		log.Println("send healthy endpoints")
		w.Header().Set("Content-Type", "application/json")
		b, err := json.Marshal(healthyEndpoints)
		if err != nil {
			log.Println("error in marshalling healthy endpoints")
		}
		w.Write(b)

	})

	log.Fatal(http.ListenAndServeTLS(":9443", "/go/src/healthcheck/wildcard.crt",
		"/go/src/healthcheck/wildcard.key", nil))
}

func performHealthChecks(localRegion string, retries int, interval time.Duration) {
	caCert, err := os.ReadFile("/go/src/healthcheck/wildcard-ca.crt")
	if err != nil {
		log.Fatalf("Failed to read CA certificate: %v", err)
	}

	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)

	tlsConfig := &tls.Config{
		//RootCAs: caCertPool,
		InsecureSkipVerify: true,
	}

	client := http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}

	for {
		// Perform health checks for each endpoint asynchronously
		endpointsMutex.RLock()
		for region, endpoints := range endpoints {
			go func(region string, endpoints []string) {
				for _, endpoint := range endpoints {
					// Prepare the URL for health check
					var url string
					if region == localRegion {
						// Local region health check
						epStatus, localhealth := getLocalClusterHealth(localRegion)
						localHostCacheMutex.Lock()
						localHostHealth = localhealth
						localHostCacheMutex.Unlock()

						cacheMutex.Lock()
						status := statusCache[endpoint]
						status.Healthy = epStatus
						if epStatus {
							status.Retries = 0
							status.LastCheckTime = time.Now()
						}
						statusCache[endpoint] = status
						cacheMutex.Unlock()
						continue
					}
					// Remote region health check
					url = fmt.Sprintf("https://%s:9443/health/%s", endpoint, region)

					// Perform retries
					for i := 0; i <= retries; i++ {
						resp, err := client.Get(url)
						log.Println("GET RESPONSE", resp, err)
						// Update the status in the cache
						cacheMutex.Lock()
						status := statusCache[endpoint]
						if err == nil && resp.StatusCode == http.StatusOK {
							log.Println("Ok")
							// If healthy, reset retries and update timestamp
							status.Healthy = true
							status.Retries = 0
							status.LastCheckTime = time.Now()
						} else {
							// If unhealthy, increase retries and mark as unhealthy after a certain number of retries
							status.Retries++
							if status.Retries >= retries {
								log.Println("Not Ok")
								status.Healthy = false
							}
						}
						statusCache[endpoint] = status
						cacheMutex.Unlock()

						if status.Healthy {
							break // Exit retry loop if healthy
						} else if i < retries {
							time.Sleep(interval) // Wait before the next retry
						}
					}
				}
			}(region, endpoints)
		}
		endpointsMutex.RUnlock()

		// Perform health checks at the specified interval
		log.Printf("Perform health checks at the specified interval %v", interval)
		time.Sleep(interval)
	}
}

func populateHostBasedStatus(localRegion string, retries int, interval time.Duration) {
	caCert, err := os.ReadFile("/go/src/healthcheck/wildcard-ca.crt")
	if err != nil {
		log.Fatalf("Failed to read CA certificate: %v", err)
	}

	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)

	tlsConfig := &tls.Config{
		//RootCAs: caCertPool,
		InsecureSkipVerify: true,
	}

	client := http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}

	for {
		// Perform health checks for each endpoint asynchronously
		endpointsMutex.RLock()
		for region, endpoints := range endpoints {
			go func(region string, endpoints []string) {
				for _, endpoint := range endpoints {
					// Remote region health check
					url := fmt.Sprintf("https://%s:9443/localhealth/", endpoint)

					// Perform retries
					for i := 0; i <= retries; i++ {
						resp, err := client.Get(url)
						log.Println("GET RESPONSE", resp, err)

						// Update the status in the cache
						hostCacheMutex.Lock()
						if err == nil && resp.StatusCode == http.StatusOK {
							bodyBytes, err := io.ReadAll(resp.Body)
							if err != nil {
								log.Println("error in reading body", err)
								i++
								continue
							}
							bodyString := string(bodyBytes)
							hostStatus := &map[string]bool{}
							err = json.Unmarshal(bodyBytes, hostStatus)
							if err != nil {
								log.Println("error in unmarshaling body", err)
								i++
								continue
							}
							for host, healthy := range *hostStatus {
								if _, ok := hostBasedStatusCache[host]; !ok {
									hostBasedStatusCache[host] = make(map[string]EndpointStatus)
								}
								hostBasedStatus := hostBasedStatusCache[host]
								hostBasedStatus[endpoint] = EndpointStatus{
									Healthy:       healthy,
									Retries:       0,
									LastCheckTime: time.Now(),
								}
								hostBasedStatusCache[host] = hostBasedStatus
							}
							log.Println(bodyString)
							log.Println("Ok")
							// populate hostBasedHealthStatus
						}
						hostCacheMutex.Unlock()

					}
				}
			}(region, endpoints)
		}

		endpointsMutex.RUnlock()
		// Perform health checks at the specified interval
		log.Printf("Perform health checks at the specified interval %v", interval)
		time.Sleep(interval)
	}
}

// (TODO)optimize this function
func getLocalClusterHealth(region string) (bool, map[string]bool) {
	resp, err := http.Get("http://localhost:15000/clusters")
	if err != nil {
		fmt.Println("Error sending GET request:", err)
		return false, nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("Error reading response body:", err)
		return false, nil
	}
	lines := strings.Split(string(body), "\n")
	resp.Body.Close()
	hostStatus := make(map[string]bool)

	count := 0
	for _, line := range lines {
		endpointsMutex.RLock()
		if regionRegex[region].MatchString(line) {
			if regexAfter.MatchString(line) && regexBefore.MatchString(line) {
				line = string(regexAfter.ReplaceAll([]byte(line), []byte("")))
				line = string(regexBefore.ReplaceAll([]byte(line), []byte("")))
				fmt.Println(line)
				hostStatus[line] = true
			}
			count++
		}
		endpointsMutex.RUnlock()
	}
	fmt.Println("Number of lines with both matches:", count)
	return count != 0, hostStatus
}

func populateTier1Endpoints() {
	for {
		resp, err := http.Get(xcpEdgeDebugEndpoint + "/debug/clusters")
		if err != nil {
			fmt.Println(fmt.Sprintf("Error sending GET request: %s", err.Error()))
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			fmt.Println(fmt.Sprintf("Error reading response body: %s", err.Error()))
		}

		xcpClusters := []xcp.Cluster{}
		err = json.Unmarshal(body, &xcpClusters)
		if err != nil {
			fmt.Println(fmt.Sprintf("Error unmarshalling body: %s", err.Error()))
		}

		endpointsMutex.Lock()
		defer endpointsMutex.Unlock()

		for _, cluster := range xcpClusters {
			for _, ns := range cluster.GetGwHostNamespaces() {
				if ns.GetName() == tier1GatewayNamespace {
					for _, svc := range ns.GetServices() {
						for k, v := range svc.GetSelector() {
							if k+":"+v == tier1GatewaySelector {
								region := cluster.GetState().GetDiscoveredLocality().GetRegion()
								if region == "" {
									continue
								}

								if _, ok := regionRegex[region]; !ok {
									regionRegex[region] = regexp.MustCompile(region)
									allRegions = append(allRegions, region)
								}
								endpoints[region] = append(endpoints[region], svc.GetKubernetesExternalAddresses()...)
							}
						}
					}
				}
			}
		}

		time.Sleep(30 * time.Second)
	}
}
