package main

import (
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type EndpointStatus struct {
	Healthy       bool
	Retries       int
	LastCheckTime time.Time
}

var (
	statusCache     map[string]EndpointStatus
	cacheMutex      sync.RWMutex
	defaultRetries  = 3
	defaultInterval = 2 * time.Second
	endpoints       = map[string][]string{
		"eastus":    {"10.160.1.75"},
		"centralus": {"10.158.0.223"},
	}
)

func main() {
	localRegion := os.Getenv("REGION")
	localHealthCheckPort := os.Getenv("LOCAL_HEALTHCHECK_PORT")
	remoteHealthCheckPort := os.Getenv("REMOTE_HEALTHCHECK_PORT")
	retriesEnv := os.Getenv("RETRIES")
	intervalEnv := os.Getenv("HEALTH_CHECK_INTERVAL")

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

	// Start a goroutine to perform asynchronous health checks
	go performHealthChecks(localRegion, localHealthCheckPort, remoteHealthCheckPort, retries, interval)

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

		log.Println("regions to be checked before checking the local endpoints ", regionsTobeChecked)
		// Check if any endpoint in the requested regions is healthy
		anyHealthy := false
		cacheMutex.RLock()
		for _, region := range regionsTobeChecked {
			for _, endpoint := range endpoints[region] {
				status, ok := statusCache[endpoint]
				if ok && status.Healthy && time.Since(status.LastCheckTime) < cachedStatusWindow {
					log.Printf("endpoint %s found healthy in region %s", endpoint, region)
					anyHealthy = true
					break
				}
			}
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

	log.Fatal(http.ListenAndServeTLS(":9443", "/go/src/healthcheck/wildcard.crt",
		"/go/src/healthcheck/wildcard.key", nil))
}

func performHealthChecks(localRegion, localHealthCheckPort, remoteHealthCheckPort string, retries int, interval time.Duration) {
	d := net.Dialer{Timeout: 3 * time.Second}

	for {
		// Perform health checks for each endpoint asynchronously
		for region, endpoints := range endpoints {
			go func(region string, endpoints []string) {
				for _, endpoint := range endpoints {
					address := endpoint + ":" + remoteHealthCheckPort
					if region == localRegion {
						address = "127.0.0.1:" + localHealthCheckPort
					}

					// Perform retries
					for i := 0; i <= retries; i++ {
						_, err := d.Dial("tcp", address)

						// Update the status in the cache
						cacheMutex.Lock()
						status := statusCache[endpoint]
						if err == nil {
							// If healthy, reset retries and update timestamp
							status.Healthy = true
							status.Retries = 0
							status.LastCheckTime = time.Now()
						} else {
							// If unhealthy, increase retries and mark as unhealthy after a certain number of retries
							status.Retries++
							if status.Retries >= retries {
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

		// Perform health checks at the specified interval
		log.Printf("Perform health checks at the specified interval %v", interval)
		time.Sleep(interval)
	}
}
