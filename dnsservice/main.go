package main

import (
	"crypto/tls"
	dnsendpoint "dnsservice/dnsendpoints"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	ctrlClient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

const (
	extDNSObjName = "t1-hosts"
)

var (
	interval               time.Duration
	healthCheckSvcEndpoint string
	defaultInterval        = 2 * time.Second
	healthyEndpointsHash   string
)

func main() {
	priority := os.Getenv("PRIORITY")

	healthCheckSvcEndpoint = os.Getenv("HEALTH_CHECK_SVC_ENDPOINT")
	url := "https://" + healthCheckSvcEndpoint + "/endpoints/" + priority

	intervalEnv := os.Getenv("HEALTH_CHECK_INTERVAL")
	interval = defaultInterval

	extDNSNamespace := os.Getenv("EXTERNAL_DNS_NAMESPACE")

	dnsTTL := os.Getenv("DNS_TTL")
	intDNSTTL, err := strconv.Atoi(dnsTTL)

	if err != nil {
		log.Fatalln("Error converting DNS_TTL to int", err)
	}

	cfg, err := config.GetConfig()
	if err != nil {
		log.Fatalf("Error getting in-cluster config: %v", err)
	}

	kubeClient, err := ctrlClient.New(cfg, ctrlClient.Options{})
	if err != nil {
		log.Fatalf("Error creating client: %v", err)
	}

	if intervalEnv != "" {
		intervalDuration, err := time.ParseDuration(intervalEnv)
		if err == nil {
			interval = intervalDuration
		}
	}
	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
	}

	client := http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}

	numberOfContinousErrors := 0
	healthyEndpointsHash = ""
	for {
		resp, err := client.Get(url)
		log.Println("GET RESPONSE", resp, err)
		if err != nil {
			numberOfContinousErrors++
			if numberOfContinousErrors >= 3 {
				// Deleting DNSEndpoints as Tier1 is down but ext dns is up
				healthyEndpointsHash = ""
				dnsendpoint.DeleteDnsEndpoint(kubeClient, extDNSNamespace, extDNSObjName)
			}
			continue
		}
		b, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			log.Println("error reading response body", err)
		}

		if isEqual(b) {
			log.Println("no change in healthy endpoints")
			time.Sleep(interval)
			continue
		}

		if healthyEndpointsHash == "" {
			err = dnsendpoint.GetDnsEndpoint(kubeClient, extDNSNamespace, extDNSObjName)
			if err != nil {
				fmt.Println(err)
				err = dnsendpoint.CreateDnsEndpoint(kubeClient, extDNSNamespace, extDNSObjName)
				if err != nil {
					log.Printf("error in creating dns endpoint resource: %v", err)
					time.Sleep(interval)
					continue

				}
			}

		}
		healthyEndpointsHash = calculateHash(b)
		fetchedHealthyEndpoints := map[string][]string{}
		err = json.Unmarshal(b, &fetchedHealthyEndpoints)
		if err != nil {
			log.Printf("error unmarshalling json: %v", err)
			time.Sleep(interval)
			continue
		}
		resp.Body.Close()

		endpoints := []map[string]interface{}{}
		for fqdn, targets := range fetchedHealthyEndpoints {
			endpoints = append(endpoints, map[string]interface{}{
				"dnsName":    fqdn,
				"recordTTL":  intDNSTTL,
				"recordType": "A",
				"targets":    targets,
			})
		}
		err = dnsendpoint.UpdateDnsEndpoint(kubeClient, extDNSNamespace, extDNSObjName, endpoints)
		if err != nil {
			log.Println("error updating DNSEndpoint", err)
		}
		time.Sleep(interval)
	}
}

func calculateHash(in []byte) string {
	hash := fnv.New64a()
	_, _ = hash.Write(in)
	out := hash.Sum(make([]byte, 0, 8))
	return hex.EncodeToString(out)
}
func isEqual(fetchedHealthyEndpoints []byte) bool {
	return calculateHash(fetchedHealthyEndpoints) == healthyEndpointsHash
}
