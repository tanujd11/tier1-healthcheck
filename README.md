Build and push t1-healthcheck image

`docker build --push -t t1failovergw.azurecr.io/t1-healthcheck:test-4 -f Dockerfile .  `

Steps to use:

1. Install bookinfo app:

`kubectl apply -f t1-failover-azure/bookinfo.yaml -n bookinfo-2`

2. Install Tier2s:

`kubectl apply -f t2s/tier2-install-bookinfo-2.yaml -n bookinfo-2`

3. Install TSB components:

```
tctl apply -f t1-failover-azure/tsb-ws-gg.yaml  
tctl apply -f t1-failover-azure/igw.yaml  
```

4. Install envoyfilter after updating namespace:

`kubectl apply -f envoyfilter.yaml`