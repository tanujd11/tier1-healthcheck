package dnsendpoint

import (
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrlClient "sigs.k8s.io/controller-runtime/pkg/client"
)

func getBaseDNSEndpointObj(name string, namespace string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetName(name)
	u.SetNamespace(namespace)
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "externaldns.k8s.io",
		Kind:    "DNSEndpoint",
		Version: "v1alpha1",
	})

	return u
}

func CreateDnsEndpoint(client ctrlClient.Client, namespace, name string) error {
	ctx := context.Background()
	u := getBaseDNSEndpointObj(name, namespace)
	err := client.Create(ctx, u)
	return err
}

func DeleteDnsEndpoint(client ctrlClient.Client, namespace, name string) error {
	ctx := context.Background()
	u := getBaseDNSEndpointObj(name, namespace)
	err := client.Delete(ctx, u)
	return err
}

func UpdateDnsEndpoint(client ctrlClient.Client, namespace, name string, endpoints []map[string]interface{}) error {
	ctx := context.Background()
	u := getBaseDNSEndpointObj(name, namespace)
	err := client.Get(ctx, ctrlClient.ObjectKey{
		Namespace: namespace,
		Name:      name,
	}, u)
	if err != nil {
		return err
	}
	u.UnstructuredContent()["spec"] = map[string]interface{}{
		"endpoints": endpoints,
	}
	err = client.Update(ctx, u)
	return err
}
