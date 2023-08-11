package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	localRegion := os.Getenv("REGION")
	localHealthCheckPort := os.Getenv("LOCAL_HEALTHCHECK_PORT")
	remoteHealthCheckPort := os.Getenv("REMOTE_HEALTHCHECK_PORT")

	fmt.Println("envs: ", localRegion, localHealthCheckPort, remoteHealthCheckPort)
	endpoints := map[string][]string{
		"us-east-1":      {"10.150.0.5"},
		"ap-southeast-1": {"10.153.65.75"},
	}

	http.HandleFunc("/health/", func(w http.ResponseWriter, r *http.Request) {
		region := strings.TrimPrefix(r.URL.Path, "/health/")
		fmt.Println("region: ", region)
		d := net.Dialer{Timeout: 3 * time.Second}
		if localRegion == region {
			_, err := d.Dial("tcp", "127.0.0.1:"+localHealthCheckPort)
			fmt.Println("err_1: ", err)

			if err != nil {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
		} else {
			_, err := d.Dial("tcp", "127.0.0.1:"+localHealthCheckPort)
			fmt.Println("err_2: ", err)
			if err != nil {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			for _, endpoint := range endpoints[region] {
				_, err := d.Dial("tcp", endpoint+":"+remoteHealthCheckPort)
				fmt.Println("remote: ", endpoint+":"+remoteHealthCheckPort)
				fmt.Println("err: ", err)
				if err == nil {
					w.WriteHeader(http.StatusBadGateway)
					return
				}
			}
		}
		w.WriteHeader(http.StatusOK)
	})

	log.Fatal(http.ListenAndServe(":9443", nil))
}
