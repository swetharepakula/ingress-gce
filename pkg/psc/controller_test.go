/*
Copyright 2020 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package psc

import (
	"context"
	"reflect"
	"testing"

	"github.com/GoogleCloudPlatform/k8s-cloud-provider/pkg/cloud"
	"github.com/GoogleCloudPlatform/k8s-cloud-provider/pkg/cloud/meta"
	alpha "google.golang.org/api/compute/v0.alpha"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	sav1alpha1 "k8s.io/ingress-gce/pkg/apis/serviceattachment/v1alpha1"
	"k8s.io/ingress-gce/pkg/composite"
	"k8s.io/ingress-gce/pkg/utils/namer"
	"k8s.io/kubernetes/pkg/apis/core"
	"k8s.io/legacy-cloud-providers/gce"
)

// func getFakeGCECloud(vals gce.TestClusterValues) *gce.Cloud {
// 	fakeGCE := gce.NewFakeGCECloud(vals)

// // 	GetHook    func(ctx context.Context, key *meta.Key, m *MockAlphaServiceAttachments) (bool, *alpha.ServiceAttachment, error)
// // 	ListHook   func(ctx context.Context, fl *filter.F, m *MockAlphaServiceAttachments) (bool, []*alpha.ServiceAttachment, error)
// // 	InsertHook func(ctx context.Context, key *meta.Key, obj *alpha.ServiceAttachment, m *MockAlphaServiceAttachments) (bool, error)
// // 	DeleteHook func(ctx context.Context, key *meta.Key, m *MockAlphaServiceAttachments) (bool, error)
// 	// InsertHook required to assign an IP Address for forwarding rule
// 	// (fakeGCE.Compute().(*cloud.MockGCE)).MockAlphaServiceAttachments.InsertHook = mock.InsertAddressHook
// 	// (fakeGCE.Compute().(*cloud.MockGCE)).MockAlphaAddresses.X = mock.AddressAttributes{}
// 	// (fakeGCE.Compute().(*cloud.MockGCE)).MockAddresses.X = mock.AddressAttributes{}
// 	// (fakeGCE.Compute().(*cloud.MockGCE)).MockForwardingRules.InsertHook = mock.InsertFwdRuleHook

// 	// (fakeGCE.Compute().(*cloud.MockGCE)).MockRegionBackendServices.UpdateHook = mock.UpdateRegionBackendServiceHook
// 	// (fakeGCE.Compute().(*cloud.MockGCE)).MockHealthChecks.UpdateHook = mock.UpdateHealthCheckHook
// 	// (fakeGCE.Compute().(*cloud.MockGCE)).MockFirewalls.UpdateHook = mock.UpdateFirewallHook
// 	return fakeGCE
// }

const (
	ClusterID = "cluster-id"
)

func TestCreation(t *testing.T) {
	testNamespace := "test-namespace"
	saName := "my-sa"
	svcName := "my-service"
	frName := "test-fr"

	svc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: testNamespace,
			Name:      svcName,
		},
		Status: v1.ServiceStatus{
			LoadBalancer: v1.LoadBalancerStatus{
				Ingress: []v1.LoadBalancerIngress{
					{
						IP: "1.2.3.4",
					},
				},
			},
		},
	}
	kubeClient := fake.NewSimpleClientset()
	kubeClient.CoreV1().Services(testNamespace).Create(context.TODO(), svc, metav1.CreateOptions{})

	fakeCloud := gce.NewFakeGCECloud(gce.DefaultTestClusterValues())
	key, err := composite.CreateKey(fakeCloud, frName, meta.Zonal)
	if err != nil {
		t.Errorf("Unexpected error when creating key - %v", err)
	}

	namer := namer.NewNamer(ClusterID, "")

	// Create a ForwardingRule that matches
	fwdRule := &composite.ForwardingRule{
		Name:                frName,
		IPAddress:           "1.2.3.4",
		Ports:               []string{"123"},
		IPProtocol:          "TCP",
		LoadBalancingScheme: string(cloud.SchemeInternal),
	}
	if err = composite.CreateForwardingRule(fakeCloud, key, fwdRule); err != nil {
		t.Errorf("Failed to create fake forwarding rule %s, err %v", fwdRule, err)
	}

	rule, err := composite.GetForwardingRule(fakeCloue, key, meta.VersionGA)(*ForwardingRule, error)
	if err != nil {
		t.Errorf("Failed to get forwarding rule, err: %s", err)
	}

	saCR := &sav1alpha1.ServiceAttachment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: testNamespace,
			Name:      saName,
		},
		Spec: sav1alpha1.ServiceAttachmentSpec{
			ConnectionPreference: "acceptAutomatic",
			NATSubnets:           []string{"my-subnet"},
			ResourceReference:    &core.TypedLocalObjectReference{},
		},
	}

	expectedSA := &alpha.ServiceAttachment{
		ConnectionPreference: "ACCEPT_AUTOMATIC",

		Description: "",
		Name:        namer.ServiceAttachment(testNamespace, saName),

		NatSubnets: []string{"my-subnet"},

		ProducerForwardingRule: rule.Selflink,
	}

	saKey, err := composite.CreateKey(fakeCloud, namer.ServiceAttachment(testNamespace, saName), meta.Zonal)
	if err != nil {
		t.Errorf("errored creating a key for service attachment")
	}
	saCloud := fakeCloud.Compute().AlphaServiceAttachments()

	sa, err := saCloud.Get(context.TODO(), saKey)
	if err != nil {
		t.Errorf("errored querying for service attachment")
	}

	if !reflect.DeepEqual(sa, expectedSA) {
		t.Errorf("Expected service attachment resource to be %+v, but found %+v", expectedSA, sa)
	}

}
