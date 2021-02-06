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
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/k8s-cloud-provider/pkg/cloud"
	"github.com/GoogleCloudPlatform/k8s-cloud-provider/pkg/cloud/meta"
	alpha "google.golang.org/api/compute/v0.alpha"
	ga "google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	cloudprovider "k8s.io/cloud-provider"
	"k8s.io/ingress-gce/pkg/annotations"
	sav1alpha1 "k8s.io/ingress-gce/pkg/apis/serviceattachment/v1alpha1"
	"k8s.io/ingress-gce/pkg/composite"
	"k8s.io/ingress-gce/pkg/context"
	safake "k8s.io/ingress-gce/pkg/serviceattachment/client/clientset/versioned/fake"
	"k8s.io/ingress-gce/pkg/test"
	"k8s.io/ingress-gce/pkg/utils"
	"k8s.io/ingress-gce/pkg/utils/common"
	"k8s.io/ingress-gce/pkg/utils/namer"
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
	apiGroup := "v1"
	validRef := sav1alpha1.ResourceReference{
		APIGroup: &apiGroup,
		Kind:     "service",
		Name:     svcName,
	}
	frIPAddr := "1.2.3.4"

	testCases := []struct {
		desc                 string
		annotationKey        string
		legacySvc            bool
		svcExists            bool
		fwdRuleExists        bool
		connectionPreference string
		resourceRef          sav1alpha1.ResourceReference
		incorrectIPAddr      bool
		invalidSubnet        bool
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
			desc:                 "legacy ILB service",
			legacySvc:            true,
			svcExists:            true,
			fwdRuleExists:        true,
			connectionPreference: "acceptAutomatic",
			resourceRef:          validRef,
			expectErr:            false,
		},
		{
			desc:                 "legacy ILB service, forwarding rule has wrong IP",
			legacySvc:            true,
			svcExists:            true,
			fwdRuleExists:        true,
			connectionPreference: "acceptAutomatic",
			resourceRef:          validRef,
			incorrectIPAddr:      true,
			expectErr:            true,
		},
		{
			desc:                 "forwarding rule has wrong IP",
			annotationKey:        annotations.TCPForwardingRuleKey,
			svcExists:            true,
			fwdRuleExists:        true,
			connectionPreference: "acceptAutomatic",
			resourceRef:          validRef,
			incorrectIPAddr:      true,
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
			desc:                 "legacy ILB service, forwarding rule does not exist",
			legacySvc:            true,
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
			resourceRef: sav1alpha1.ResourceReference{
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

		var frName string
		if tc.svcExists {
			ipAddr := frIPAddr
			if tc.incorrectIPAddr {
				ipAddr = "5.6.7.8"
			}

			var err error
			if tc.legacySvc {
				_, frName, err = createLegacyILBSvc(controller, svcName, "svc-uid", ipAddr)
				if err != nil {
					t.Errorf("%s:%s", tc.desc, err)
				}
			} else {
				_, frName, err = createSvc(controller, svcName, tc.annotationKey, ipAddr)
				if err != nil {
					t.Errorf("%s:%s", tc.desc, err)
				}
			}
		}

		var rule *composite.ForwardingRule
		if tc.fwdRuleExists {
			var err error
			if rule, err = createForwardingRule(fakeCloud, frName, frIPAddr); err != nil {
				t.Errorf("%s: %s", tc.desc, err)
			}
		}

		subnetURL := ""
		if !tc.invalidSubnet {
			if subnet, err := createNatSubnet(fakeCloud, "my-subnet"); err != nil {
				t.Errorf("%s: %s", tc.desc, err)
			} else {
				subnetURL = subnet.SelfLink
			}
		}

		saCR := &sav1alpha1.ServiceAttachment{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:       testNamespace,
				Name:            saName,
				UID:             "service-attachment-uid",
				ResourceVersion: "resource-version",
			},
			Spec: sav1alpha1.ServiceAttachmentSpec{
				ConnectionPreference: tc.connectionPreference,
				NATSubnets:           []string{"my-subnet"},
				ResourceReference:    tc.resourceRef,
			},
		}

		controller.svcAttachmentLister.Add(saCR)
		controller.saClient.NetworkingV1alpha1().ServiceAttachments(testNamespace).Create(context2.TODO(), saCR, metav1.CreateOptions{})

		err := controller.processServiceAttachment(SvcAttachmentKeyFunc(testNamespace, saName))
		if tc.expectErr && err == nil {
			t.Errorf("%s: expected an error when process service attachment", tc.desc)
		} else if !tc.expectErr && err != nil {
			t.Errorf("%s: unexpected error processing Service Attachment: %s", tc.desc, err)
		} else if !tc.expectErr {

			updatedCR, err := controller.saClient.NetworkingV1alpha1().ServiceAttachments(testNamespace).Get(context2.TODO(), saName, metav1.GetOptions{})
			if err != nil {
				t.Errorf("%s: unexpected error while querying for service attachment %s: %q", tc.desc, saName, err)
			}

			if updatedCR.ResourceVersion != saCR.ResourceVersion {
				t.Errorf("%s: Resource versions should not change when Service Attachment CR is updated", tc.desc)
			}

			if err = verifyServiceAttachmentFinalizer(updatedCR); err != nil {
				t.Errorf("%s:%s", tc.desc, err)
			}

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
				NatSubnets:             []string{subnetURL},
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
	frIPAddr := "1.2.3.4"

	saCRAnnotation := testServiceAttachmentCR(saName, svcName, saUID, []string{"my-subnet"}, true)
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
			updatedSACR: testServiceAttachmentCR(saName, svcName, saUID, []string{"diff-subnet"}, true),
			expectErr:   true,
		},
		{
			desc:        "updated service name",
			updatedSACR: testServiceAttachmentCR(saName, "my-second-service", saUID, []string{"my-subnet"}, true),
			expectErr:   true,
		},
		{
			desc:        "removed finalizer",
			updatedSACR: testServiceAttachmentCR(saName, svcName, saUID, []string{"my-subnet"}, false),
			expectErr:   false,
		},
	}

	for _, tc := range testcases {
		controller := newTestController()
		gceSAName := controller.saNamer.ServiceAttachment(testNamespace, saName, saUID)
		_, frName, err := createSvc(controller, svcName, annotations.TCPForwardingRuleKey, frIPAddr)
		if err != nil {
			t.Errorf("%s:%s", tc.desc, err)
		}
		if _, err = createForwardingRule(controller.cloud, frName, frIPAddr); err != nil {
			t.Errorf("%s: %s", tc.desc, err)
		}

		saCR := testServiceAttachmentCR(saName, svcName, saUID, []string{"my-subnet"}, false)
		err = controller.svcAttachmentLister.Add(saCR)
		if err != nil {
			t.Fatalf("%s: Failed to add service attachment cr to store: %q", tc.desc, err)
		}
		_, err = controller.saClient.NetworkingV1alpha1().ServiceAttachments(testNamespace).Create(context2.TODO(), saCR, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("%s: Failed to create service attachment cr: %q", tc.desc, err)
		}

		if err := controller.processServiceAttachment(SvcAttachmentKeyFunc(testNamespace, saName)); err != nil {
			t.Fatalf("%s: Unexpected error while processing ServiceAttachment: %q", tc.desc, err)
		}

		createdSA, err := getServiceAttachment(controller.cloud, gceSAName)
		if err != nil {
			t.Fatalf("%s: Unexpected error when getting GCE ServiceAttachment: %q", tc.desc, err)
		}

		if saCR.Spec.ResourceReference.Name != tc.updatedSACR.Spec.ResourceReference.Name {
			if _, frName, err = createSvc(controller, tc.updatedSACR.Spec.ResourceReference.Name, annotations.TCPForwardingRuleKey, frIPAddr); err != nil {
				t.Fatalf("%s:%s", tc.desc, err)
			}
			if _, err = createForwardingRule(controller.cloud, frName, frIPAddr); err != nil {
				t.Fatalf("%s: %s", tc.desc, err)
			}
		}

		err = controller.svcAttachmentLister.Add(tc.updatedSACR)
		if err != nil {
			t.Fatalf("%s: Failed to add tc.updatedSACR to store: %q", tc.desc, err)
		}
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

		saCR, err = controller.saClient.NetworkingV1alpha1().ServiceAttachments(testNamespace).Get(context2.TODO(), saName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("%s: Failed to get service attachment cr: %q", tc.desc, err)
		}

		if err = verifyServiceAttachmentFinalizer(saCR); err != nil {
			t.Errorf("%s:%s", tc.desc, err)
		}
	}
}

func TestServiceAttachmentGarbageCollection(t *testing.T) {
	svcNamePrefix := "my-service"
	saUIDPrefix := "serivce-attachment-uid"
	frIPAddr := "1.2.3.4"

	sa1 := testServiceAttachmentCR("sa-1", svcNamePrefix+"1", saUIDPrefix+"-1", []string{"my-subnet"}, true)
	sa2 := testServiceAttachmentCR("sa-2", svcNamePrefix+"2", saUIDPrefix+"-2", []string{"my-subnet"}, true)
	sa3 := testServiceAttachmentCR("sa-3", svcNamePrefix+"3", saUIDPrefix+"-3", []string{"my-subnet"}, true)
	sa4 := testServiceAttachmentCR("sa-4", svcNamePrefix+"4", saUIDPrefix+"-4", []string{"my-subnet"}, true)
	sa5 := testServiceAttachmentCR("sa-5", svcNamePrefix+"5", saUIDPrefix+"-5", []string{"my-subnet"}, true)

	controller := newTestController()

	for _, sa := range []*sav1alpha1.ServiceAttachment{sa1, sa2, sa3, sa4, sa5} {
		svcName := sa.Spec.ResourceReference.Name
		svc, frName, err := createSvc(controller, svcName, annotations.TCPForwardingRuleKey, frIPAddr)
		if err != nil {
			t.Errorf("%s", err)
		}
		controller.serviceLister.Add(svc)

		if _, err = createForwardingRule(controller.cloud, frName, frIPAddr); err != nil {
			t.Errorf("%s", err)
		}
		controller.svcAttachmentLister.Add(sa)

		_, err = controller.saClient.NetworkingV1alpha1().ServiceAttachments(sa.Namespace).Create(context2.TODO(), sa, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("failed to add service attachment to client: %q", err)
		}

		err = controller.processServiceAttachment(SvcAttachmentKeyFunc(sa.Namespace, sa.Name))
		if err != nil {
			t.Fatalf("failed to process service attachment: %q", err)
		}
	}

	syncServiceAttachmentLister(controller)
	deletionTS := metav1.Now()
	for _, sa := range []*sav1alpha1.ServiceAttachment{sa1, sa2, sa3, sa4} {
		saCR, err := controller.saClient.NetworkingV1alpha1().ServiceAttachments(sa.Namespace).Get(context2.TODO(), sa.Name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("Failed to get service attachment cr: %q", err)
		}

		saCR.DeletionTimestamp = &deletionTS
		_, err = controller.saClient.NetworkingV1alpha1().ServiceAttachments(sa.Namespace).Update(context2.TODO(), saCR, metav1.UpdateOptions{})
		if err != nil {
			t.Fatalf("failed to update service attachment to client: %q", err)
		}
	}

	syncServiceAttachmentLister(controller)

	fakeGCE := controller.cloud.Compute().(*cloud.MockGCE)
	mockSA := fakeGCE.AlphaServiceAttachments().(*cloud.MockAlphaServiceAttachments)

	gceSAName := controller.saNamer.ServiceAttachment(sa2.Namespace, sa2.Name, string(sa2.UID))
	saKey, _ := composite.CreateKey(controller.cloud, gceSAName, meta.Regional)
	mockSA.GetError[*saKey] = utils.FakeGoogleAPINotFoundErr()

	gceSAName = controller.saNamer.ServiceAttachment(sa3.Namespace, sa3.Name, string(sa3.UID))
	saKey, _ = composite.CreateKey(controller.cloud, gceSAName, meta.Regional)
	mockSA.GetError[*saKey] = &googleapi.Error{Code: http.StatusBadRequest}

	gceSAName = controller.saNamer.ServiceAttachment(sa4.Namespace, sa4.Name, string(sa4.UID))
	saKey, _ = composite.CreateKey(controller.cloud, gceSAName, meta.Regional)
	mockSA.DeleteError[*saKey] = fmt.Errorf("deletion error")

	controller.garbageCollectServiceAttachments()

	for _, sa := range []*sav1alpha1.ServiceAttachment{sa1, sa2, sa3} {
		gceSAName := controller.saNamer.ServiceAttachment(sa.Namespace, sa.Name, string(sa.UID))
		if err := verifyGCEServiceAttachmentDeletion(controller, sa); err != nil {
			t.Errorf("Expected gce sa %s to be deleted : %s", gceSAName, err)
		}

		if err := verifyServiceAttachmentCRDeletion(controller, sa); err != nil {
			t.Errorf("Expected sa %s to be deleted : %s", sa.Name, err)
		}
	}

	// sa4 finalizer should not be deleted
	currSA, err := controller.saClient.NetworkingV1alpha1().ServiceAttachments(sa4.Namespace).Get(context2.TODO(), sa4.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to query for service attachment %s from client: %q", currSA.Name, err)
	}

	if err := verifyServiceAttachmentFinalizer(currSA); err != nil {
		t.Errorf("service attachment %s finalizer should not be removed after gc: %q", currSA.Name, err)
	}

	// verify service attachments without deletion timestamp still exist
	currSA5, err := controller.saClient.NetworkingV1alpha1().ServiceAttachments(sa3.Namespace).Get(context2.TODO(), sa5.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to query for service attachment %s from client: %q", sa3.Name, err)
	}

	if err = verifyServiceAttachmentFinalizer(currSA5); err != nil {
		t.Errorf("service attachment %s finalizer should not be removed after gc: %q", sa4.Name, err)
	}

	gceSAName = controller.saNamer.ServiceAttachment(sa5.Namespace, sa5.Name, string(sa5.UID))
	gceSA, err := getServiceAttachment(controller.cloud, gceSAName)
	if err != nil {
		t.Errorf("Unexpected error when getting updated GCE ServiceAttachment: %q", err)
	}

	if gceSA == nil {
		t.Errorf("service attachment %s should not have been gc'd", sa5.Name)
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

func createLegacyILBSvc(controller *Controller, svcName, svcUID, ipAddr string) (*v1.Service, string, error) {
	svc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: testNamespace,
			Name:      svcName,
			UID:       types.UID(svcUID),
		},
		Status: v1.ServiceStatus{
			LoadBalancer: v1.LoadBalancerStatus{
				Ingress: []v1.LoadBalancerIngress{
					{
						IP: ipAddr,
					},
				},
			},
		},
	}
	return svc, cloudprovider.DefaultLoadBalancerName(svc), controller.serviceLister.Add(svc)
}

func createSvc(controller *Controller, svcName, forwardingRuleKey, ipAddr string) (*v1.Service, string, error) {
	frName := svcName + "-fr"
	svcAnnotations := map[string]string{
		forwardingRuleKey: frName,
	}

	svc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   testNamespace,
			Name:        svcName,
			Annotations: svcAnnotations,
		},
		Status: v1.ServiceStatus{
			LoadBalancer: v1.LoadBalancerStatus{
				Ingress: []v1.LoadBalancerIngress{
					{
						IP: ipAddr,
					},
				},
			},
		},
	}
	return svc, frName, controller.serviceLister.Add(svc)
}

func createForwardingRule(c *gce.Cloud, frName, ipAddr string) (*composite.ForwardingRule, error) {
	key, err := composite.CreateKey(c, frName, meta.Regional)
	if err != nil {
		return nil, fmt.Errorf("Unexpected error when creating key: %q", err)
	}
	// Create a ForwardingRule that matches
	fwdRule := &composite.ForwardingRule{
		Name:                frName,
		LoadBalancingScheme: string(cloud.SchemeInternal),
		IPAddress:           ipAddr,
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

func createNatSubnet(c *gce.Cloud, natSubnet string) (*ga.Subnetwork, error) {
	key, err := composite.CreateKey(c, natSubnet, meta.Regional)
	if err != nil {
		return nil, fmt.Errorf("Unexpected error when creating key: %q", err)
	}
	// Create a ForwardingRule that matches
	subnet := &ga.Subnetwork{
		Name: natSubnet,
	}
	if err = c.Compute().Subnetworks().Insert(context2.TODO(), key, subnet); err != nil {
		return nil, fmt.Errorf("Failed to create fake subnet %s:  %q", natSubnet, err)
	}

	subnet, err = c.Compute().Subnetworks().Get(context2.TODO(), key)
	if err != nil {
		return subnet, fmt.Errorf("Failed to get forwarding rule: %q", err)
	}
	return subnet, nil
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

func testServiceAttachmentCR(saName, svcName, svcUID string, subnets []string, withFinalizer bool) *sav1alpha1.ServiceAttachment {
	apiGroup := "v1"
	return &sav1alpha1.ServiceAttachment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:  testNamespace,
			Name:       saName,
			UID:        types.UID(svcUID),
			Finalizers: []string{common.ServiceAttachmentFinalizerKey},
		},
		Spec: sav1alpha1.ServiceAttachmentSpec{
			ConnectionPreference: "acceptAutomatic",
			NATSubnets:           subnets,
			ResourceReference: sav1alpha1.ResourceReference{
				APIGroup: &apiGroup,
				Kind:     "service",
				Name:     svcName,
			},
		},
	}
}

func verifyServiceAttachmentFinalizer(cr *sav1alpha1.ServiceAttachment) error {
	finalizers := cr.GetFinalizers()
	if len(finalizers) != 1 {
		return fmt.Errorf("Expected service attachment to have one finalizer, has %d", len(finalizers))
	} else {
		if finalizers[0] != common.ServiceAttachmentFinalizerKey {
			return fmt.Errorf("Expected service attachment to have finalizer %s, but found %s", common.ServiceAttachmentFinalizerKey, finalizers[0])
		}
	}
	return nil
}

func syncServiceAttachmentLister(controller *Controller) error {
	crs, err := controller.saClient.NetworkingV1alpha1().ServiceAttachments(testNamespace).List(context2.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	for _, cr := range crs.Items {
		saCR := cr
		if err = controller.svcAttachmentLister.Add(&saCR); err != nil {
			return err
		}
	}
	return nil
}

func verifyServiceAttachmentCRDeletion(controller *Controller, sa *sav1alpha1.ServiceAttachment) error {
	currCR, err := controller.saClient.NetworkingV1alpha1().ServiceAttachments(sa.Namespace).Get(context2.TODO(), sa.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to query for service attachment from client: %q", err)
	}

	if currCR.DeletionTimestamp.IsZero() {
		return fmt.Errorf("deletion timestamp is not set on %s", sa.Name)
	}

	if err = verifyServiceAttachmentFinalizer(currCR); err == nil {
		return fmt.Errorf("service attachment %s finalizer should be removed after gc", sa.Name)
	}
	return nil
}

func verifyGCEServiceAttachmentDeletion(controller *Controller, sa *sav1alpha1.ServiceAttachment) error {
	gceSAName := controller.saNamer.ServiceAttachment(sa.Namespace, sa.Name, string(sa.UID))
	gceSA, err := getServiceAttachment(controller.cloud, gceSAName)
	if err == nil {
		return fmt.Errorf("Expected error not found when getting GCE ServiceAttachment for SA CR %s", sa.Name)
	}

	if gceSA != nil {
		return fmt.Errorf("Service attachment: %q should have been deleted", gceSAName)
	}
	return nil
}

func verifyServiceAttachmentDeletion(controller *Controller, sa *sav1alpha1.ServiceAttachment) error {
	currCR, err := controller.saClient.NetworkingV1alpha1().ServiceAttachments(sa.Namespace).Get(context2.TODO(), sa.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to query for service attachment from client: %q", err)
	}

	if err = verifyServiceAttachmentFinalizer(currCR); err == nil {
		return fmt.Errorf("service attachment %s finalizer should be removed after gc", sa.Name)
	}

	gceSAName := controller.saNamer.ServiceAttachment(sa.Namespace, sa.Name, string(sa.UID))
	gceSA, err := getServiceAttachment(controller.cloud, gceSAName)
	if err == nil {
		return fmt.Errorf("Unexpected error when getting updated GCE ServiceAttachment: %q", err)
	}

	if gceSA != nil {
		return fmt.Errorf("Service attachment: %q should have been deleted", gceSAName)
	}
	return nil
}
