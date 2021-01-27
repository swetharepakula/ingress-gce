/*
Copyright 2021 The Kubernetes Authors.

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
	context2 "context"
	"reflect"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/k8s-cloud-provider/pkg/cloud"
	"github.com/GoogleCloudPlatform/k8s-cloud-provider/pkg/cloud/meta"
	alpha "google.golang.org/api/compute/v0.alpha"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/ingress-gce/pkg/annotations"
	sav1alpha1 "k8s.io/ingress-gce/pkg/apis/serviceattachment/v1alpha1"
	"k8s.io/ingress-gce/pkg/composite"
	"k8s.io/ingress-gce/pkg/context"
	safake "k8s.io/ingress-gce/pkg/serviceattachment/client/clientset/versioned/fake"
	"k8s.io/ingress-gce/pkg/test"
	"k8s.io/ingress-gce/pkg/utils/namer"
	"k8s.io/kubernetes/pkg/apis/core"
	"k8s.io/legacy-cloud-providers/gce"
)

const (
	ClusterID     = "cluster-id"
	kubeSystemUID = "kube-system-uid"
)

func TestCreation(t *testing.T) {
	testNamespace := "test-namespace"
	saName := "my-sa"
	svcName := "my-service"
	frName := "test-fr"

	testCases := []struct {
		desc                 string
		annotationKey        string
		svcExists            bool
		fwdRuleExists        bool
		connectionPreference string
		expectErr            bool
	}{
		{
			desc:                 "valid service attachment with tcp ILB",
			annotationKey:        annotations.TCPForwardingRuleKey,
			svcExists:            true,
			fwdRuleExists:        true,
			connectionPreference: "acceptAutomatic",
			expectErr:            false,
		},
		{
			desc:                 "valid service attachment with udp ILB",
			annotationKey:        annotations.UDPForwardingRuleKey,
			svcExists:            true,
			fwdRuleExists:        true,
			connectionPreference: "acceptAutomatic",
			expectErr:            false,
		},
		{
			desc:                 "service missing ILB forwarding rule annotation",
			annotationKey:        "some-key",
			svcExists:            true,
			fwdRuleExists:        true,
			connectionPreference: "acceptAutomatic",
			expectErr:            true,
		},
		{
			desc:                 "service does not exist",
			svcExists:            false,
			connectionPreference: "acceptAutomatic",
			expectErr:            true,
		},
		{
			desc:                 "forwarding rule does not exist",
			annotationKey:        annotations.TCPForwardingRuleKey,
			svcExists:            false,
			fwdRuleExists:        true,
			connectionPreference: "acceptAutomatic",
			expectErr:            true,
		},
	}

	for _, tc := range testCases {
		controller := newTestController()
		fakeCloud := controller.cloud

		if tc.svcExists {
			svcAnnotations := map[string]string{
				tc.annotationKey: frName,
			}
			svc := &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:   testNamespace,
					Name:        svcName,
					Annotations: svcAnnotations,
				},
			}
			controller.serviceLister.Add(svc)
		}

		key, err := composite.CreateKey(fakeCloud, frName, meta.Regional)
		if err != nil {
			t.Errorf("%s: Unexpected error when creating key - %v", tc.desc, err)
		}

		var rule *composite.ForwardingRule
		if tc.fwdRuleExists {
			// Create a ForwardingRule that matches
			fwdRule := &composite.ForwardingRule{
				Name:                frName,
				LoadBalancingScheme: string(cloud.SchemeInternal),
			}
			if err = composite.CreateForwardingRule(fakeCloud, key, fwdRule); err != nil {
				t.Errorf("%s: Failed to create fake forwarding rule %s, err %v", tc.desc, frName, err)
			}

			rule, err = composite.GetForwardingRule(fakeCloud, key, meta.VersionGA)
			if err != nil {
				t.Errorf("%s: Failed to get forwarding rule, err: %s", tc.desc, err)
			}
		}

		apiGroup := "v1"
		saCR := &sav1alpha1.ServiceAttachment{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: testNamespace,
				Name:      saName,
				UID:       "service-attachment-uid",
			},
			Spec: sav1alpha1.ServiceAttachmentSpec{
				ConnectionPreference: tc.connectionPreference,
				NATSubnets:           []string{"my-subnet"},
				ResourceReference: &core.TypedLocalObjectReference{
					APIGroup: &apiGroup,
					Kind:     "service",
					Name:     svcName,
				},
			},
		}

		controller.svcAttachmentLister.Add(saCR)

		connPref, err := GetConnectionPreference(tc.connectionPreference)
		if err != nil {
			t.Errorf("%s: Failed to get connection preference: %q", tc.desc, err)
		}

		err = controller.processServiceAttachment(SvcAttachmentKeyFunc(testNamespace, saName))
		if tc.expectErr && err == nil {
			t.Errorf("%s: expected an error when process service attachment", tc.desc)
		} else if !tc.expectErr && err != nil {
			t.Errorf("%s: unexpected error processing Service Attachment: %s", tc.desc, err)
		} else if !tc.expectErr {
			gceSAName := controller.saNamer.ServiceAttachment(testNamespace, saName, string(saCR.UID))
			expectedSA := &alpha.ServiceAttachment{
				ConnectionPreference:   connPref,
				Description:            "",
				Name:                   gceSAName,
				NatSubnets:             []string{"my-subnet"},
				ProducerForwardingRule: rule.SelfLink,
				Region:                 fakeCloud.Region(),
			}

			saKey, err := composite.CreateKey(fakeCloud, gceSAName, meta.Regional)
			if err != nil {
				t.Errorf("%s: errored creating a key for service attachment", tc.desc)
			}
			saCloud := fakeCloud.Compute().AlphaServiceAttachments()

			sa, err := saCloud.Get(context2.TODO(), saKey)
			if err != nil {
				t.Errorf("%s: errored querying for service attachment", tc.desc)
			}

			expectedSA.SelfLink = sa.SelfLink

			if !reflect.DeepEqual(sa, expectedSA) {
				t.Errorf("%s: Expected service attachment resource to be \n%+v\n, but found \n%+v", tc.desc, expectedSA, sa)
			}
		}
	}
}

func newTestController() *Controller {
	kubeClient := fake.NewSimpleClientset()
	fakeGCE := gce.NewFakeGCECloud(gce.DefaultTestClusterValues())
	resourceNamer := namer.NewNamer(ClusterID, "")
	saClient := safake.NewSimpleClientset()

	ctxConfig := context.ControllerContextConfig{
		Namespace:             v1.NamespaceAll,
		ResyncPeriod:          1 * time.Minute,
		DefaultBackendSvcPort: test.DefaultBeSvcPort,
		HealthCheckPath:       "/",
	}

	ctx := context.NewControllerContext(nil, kubeClient, nil, nil, nil, nil, saClient, fakeGCE, resourceNamer, kubeSystemUID, ctxConfig)

	return NewController(ctx)
}
