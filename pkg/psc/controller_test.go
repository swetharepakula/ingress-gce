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
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/k8s-cloud-provider/pkg/cloud"
	"github.com/GoogleCloudPlatform/k8s-cloud-provider/pkg/cloud/meta"
	alpha "google.golang.org/api/compute/v0.alpha"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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
	testNamespace = "test-namespace"
)

func TestServiceAttachmentCreation(t *testing.T) {
	saName := "my-sa"
	svcName := "my-service"
	frName := "test-fr"
	apiGroup := "v1"
	validRef := &core.TypedLocalObjectReference{
		APIGroup: &apiGroup,
		Kind:     "service",
		Name:     svcName,
	}

	testCases := []struct {
		desc                 string
		annotationKey        string
		svcExists            bool
		fwdRuleExists        bool
		connectionPreference string
		resourceRef          *core.TypedLocalObjectReference
		expectErr            bool
	}{
		{
			desc:                 "valid service attachment with tcp ILB",
			annotationKey:        annotations.TCPForwardingRuleKey,
			svcExists:            true,
			fwdRuleExists:        true,
			connectionPreference: "acceptAutomatic",
			resourceRef:          validRef,
			expectErr:            false,
		},
		{
			desc:                 "valid service attachment with udp ILB",
			annotationKey:        annotations.UDPForwardingRuleKey,
			svcExists:            true,
			fwdRuleExists:        true,
			connectionPreference: "acceptAutomatic",
			resourceRef:          validRef,
			expectErr:            false,
		},
		{
			desc:                 "service missing ILB forwarding rule annotation",
			annotationKey:        "some-key",
			svcExists:            true,
			fwdRuleExists:        true,
			connectionPreference: "acceptAutomatic",
			resourceRef:          validRef,
			expectErr:            true,
		},
		{
			desc:                 "service does not exist",
			svcExists:            false,
			connectionPreference: "acceptAutomatic",
			resourceRef:          validRef,
			expectErr:            true,
		},
		{
			desc:                 "forwarding rule does not exist",
			annotationKey:        annotations.TCPForwardingRuleKey,
			svcExists:            true,
			fwdRuleExists:        false,
			connectionPreference: "acceptAutomatic",
			resourceRef:          validRef,
			expectErr:            true,
		},
		{
			desc:                 "accept automatic wrong format",
			annotationKey:        annotations.TCPForwardingRuleKey,
			svcExists:            true,
			fwdRuleExists:        true,
			connectionPreference: "ACCEPT_AUTOMATIC",
			resourceRef:          validRef,
			expectErr:            true,
		},
		{
			desc:                 "invalid connection preference",
			annotationKey:        annotations.TCPForwardingRuleKey,
			svcExists:            true,
			fwdRuleExists:        true,
			connectionPreference: "some connection preference",
			resourceRef:          validRef,
			expectErr:            true,
		},
		{
			desc:                 "invalid resource reference",
			annotationKey:        annotations.TCPForwardingRuleKey,
			svcExists:            false,
			connectionPreference: "some connection preference",
			resourceRef: &core.TypedLocalObjectReference{
				APIGroup: &apiGroup,
				Kind:     "not-service",
				Name:     svcName,
			},
			expectErr: true,
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

		var rule *composite.ForwardingRule
		if tc.fwdRuleExists {
			var err error
			if rule, err = createForwardingRule(fakeCloud, frName); err != nil {
				t.Errorf("%s: %s", tc.desc, err)
			}
		}

		saCR := &sav1alpha1.ServiceAttachment{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: testNamespace,
				Name:      saName,
				UID:       "service-attachment-uid",
			},
			Spec: sav1alpha1.ServiceAttachmentSpec{
				ConnectionPreference: tc.connectionPreference,
				NATSubnets:           []string{"my-subnet"},
				ResourceReference:    tc.resourceRef,
			},
		}

		controller.svcAttachmentLister.Add(saCR)

		err := controller.processServiceAttachment(SvcAttachmentKeyFunc(testNamespace, saName))
		if tc.expectErr && err == nil {
			t.Errorf("%s: expected an error when process service attachment", tc.desc)
		} else if !tc.expectErr && err != nil {
			t.Errorf("%s: unexpected error processing Service Attachment: %s", tc.desc, err)
		} else if !tc.expectErr {
			gceSAName := controller.saNamer.ServiceAttachment(testNamespace, saName, string(saCR.UID))
			connPref, err := GetConnectionPreference(tc.connectionPreference)
			if err != nil {
				t.Errorf("%s: Failed to get connection preference: %q", tc.desc, err)
			}
			sa, err := getServiceAttachment(fakeCloud, gceSAName)
			if err != nil {
				t.Errorf("%s: %s", tc.desc, err)
			}

			expectedSA := &alpha.ServiceAttachment{
				ConnectionPreference:   connPref,
				Description:            "",
				Name:                   gceSAName,
				NatSubnets:             []string{"my-subnet"},
				ProducerForwardingRule: rule.SelfLink,
				Region:                 fakeCloud.Region(),
				SelfLink:               sa.SelfLink,
			}

			if !reflect.DeepEqual(sa, expectedSA) {
				t.Errorf("%s: Expected service attachment resource to be \n%+v\n, but found \n%+v", tc.desc, expectedSA, sa)
			}
		}
	}
}

func TestServiceAttachmentUpdate(t *testing.T) {
	saName := "my-sa"
	svcName := "my-service"
	saUID := "serivce-attachment-uid"

	saCRAnnotation := testServiceAttachmentCR(saName, svcName, saUID, []string{"my-subnet"})
	saCRAnnotation.Annotations = map[string]string{"some-key": "some-value"}

	testcases := []struct {
		desc        string
		updatedSACR *sav1alpha1.ServiceAttachment
		expectErr   bool
	}{
		{
			desc:        "updated annotation",
			updatedSACR: saCRAnnotation,
			expectErr:   false,
		},
		{
			desc:        "updated subnet",
			updatedSACR: testServiceAttachmentCR(saName, svcName, saUID, []string{"diff-subnet"}),
			expectErr:   true,
		},
		{
			desc:        "updated service name",
			updatedSACR: testServiceAttachmentCR(saName, "my-second-service", saUID, []string{"my-subnet"}),
			expectErr:   true,
		},
	}

	for _, tc := range testcases {
		controller := newTestController()
		gceSAName := controller.saNamer.ServiceAttachment(testNamespace, saName, saUID)
		frName := "test-fr"

		svcAnnotations := map[string]string{
			annotations.TCPForwardingRuleKey: frName,
		}
		svc := &v1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:   testNamespace,
				Name:        svcName,
				Annotations: svcAnnotations,
			},
		}
		controller.serviceLister.Add(svc)
		saCR := testServiceAttachmentCR(saName, svcName, saUID, []string{"my-subnet"})
		controller.svcAttachmentLister.Add(saCR)

		if _, err := createForwardingRule(controller.cloud, frName); err != nil {
			t.Errorf("%s: %s", tc.desc, err)
		}

		if err := controller.processServiceAttachment(SvcAttachmentKeyFunc(testNamespace, saName)); err != nil {
			t.Fatalf("%s: Unexpected error while processing ServiceAttachment: %q", tc.desc, err)
		}

		createdSA, err := getServiceAttachment(controller.cloud, gceSAName)
		if err != nil {
			t.Fatalf("%s: Unexpected error when getting GCE ServiceAttachment: %q", tc.desc, err)
		}

		if saCR.Spec.ResourceReference.Name != tc.updatedSACR.Spec.ResourceReference.Name {
			frName = tc.updatedSACR.Spec.ResourceReference.Name + "-fr"
			svc2Annotations := map[string]string{
				annotations.TCPForwardingRuleKey: frName,
			}
			svc2 := &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:   testNamespace,
					Name:        tc.updatedSACR.Spec.ResourceReference.Name,
					Annotations: svc2Annotations,
				},
			}
			controller.serviceLister.Add(svc2)
			if _, err := createForwardingRule(controller.cloud, frName); err != nil {
				t.Errorf("%s: %s", tc.desc, err)
			}
		}

		controller.svcAttachmentLister.Add(tc.updatedSACR)
		err = controller.processServiceAttachment(SvcAttachmentKeyFunc(testNamespace, saName))
		if tc.expectErr && err == nil {
			t.Errorf("%s: Expected error while processing updated ServiceAttachment", tc.desc)
		} else if !tc.expectErr && err != nil {
			t.Errorf("%s: Unexpected error while processing updated ServiceAttachment: %q", tc.desc, err)
		}

		updatedSA, err := getServiceAttachment(controller.cloud, gceSAName)
		if err != nil {
			t.Fatalf("%s: Unexpected error when getting updatd GCE ServiceAttachment: %q", tc.desc, err)
		}

		if !reflect.DeepEqual(createdSA, updatedSA) {
			t.Errorf("%s: GCE Service Attachment should not be updated. \nOriginal SA:\n %+v, \nUpdated SA:\n %+v", tc.desc, createdSA, updatedSA)
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

func createForwardingRule(c *gce.Cloud, frName string) (*composite.ForwardingRule, error) {
	key, err := composite.CreateKey(c, frName, meta.Regional)
	if err != nil {
		return nil, fmt.Errorf("Unexpected error when creating key: %q", err)
	}
	// Create a ForwardingRule that matches
	fwdRule := &composite.ForwardingRule{
		Name:                frName,
		LoadBalancingScheme: string(cloud.SchemeInternal),
	}
	if err = composite.CreateForwardingRule(c, key, fwdRule); err != nil {
		return nil, fmt.Errorf("Failed to create fake forwarding rule %s:  %q", frName, err)
	}

	rule, err := composite.GetForwardingRule(c, key, meta.VersionGA)
	if err != nil {
		return rule, fmt.Errorf("Failed to get forwarding rule: %q", err)
	}
	return rule, nil
}

func getServiceAttachment(cloud *gce.Cloud, saName string) (*alpha.ServiceAttachment, error) {
	saKey, err := composite.CreateKey(cloud, saName, meta.Regional)
	if err != nil {
		return nil, fmt.Errorf("errored creating a key for service attachment: %q", err)
	}
	sa, err := cloud.Compute().AlphaServiceAttachments().Get(context2.TODO(), saKey)
	if err != nil {
		return nil, fmt.Errorf("errored querying for service attachment: %q", err)
	}
	return sa, nil
}

func testServiceAttachmentCR(saName, svcName, svcUID string, subnets []string) *sav1alpha1.ServiceAttachment {
	apiGroup := "v1"
	return &sav1alpha1.ServiceAttachment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: testNamespace,
			Name:      saName,
			UID:       types.UID(svcUID),
		},
		Spec: sav1alpha1.ServiceAttachmentSpec{
			ConnectionPreference: "acceptAutomatic",
			NATSubnets:           subnets,
			ResourceReference: &core.TypedLocalObjectReference{
				APIGroup: &apiGroup,
				Kind:     "service",
				Name:     svcName,
			},
		},
	}
}
