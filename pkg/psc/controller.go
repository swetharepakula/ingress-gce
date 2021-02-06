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
	context2 "context"
	"fmt"
	"net/http"
	"reflect"
	"time"

	"github.com/GoogleCloudPlatform/k8s-cloud-provider/pkg/cloud"
	"github.com/GoogleCloudPlatform/k8s-cloud-provider/pkg/cloud/meta"
	alpha "google.golang.org/api/compute/v0.alpha"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	cloudprovider "k8s.io/cloud-provider"
	"k8s.io/ingress-gce/pkg/annotations"
	sav1alpha1 "k8s.io/ingress-gce/pkg/apis/serviceattachment/v1alpha1"
	"k8s.io/ingress-gce/pkg/composite"
	"k8s.io/ingress-gce/pkg/context"
	serviceattachmentclient "k8s.io/ingress-gce/pkg/serviceattachment/client/clientset/versioned"
	"k8s.io/ingress-gce/pkg/utils"
	"k8s.io/ingress-gce/pkg/utils/common"
	"k8s.io/ingress-gce/pkg/utils/namer"
	"k8s.io/ingress-gce/pkg/utils/patch"
	"k8s.io/klog"
	"k8s.io/kubernetes/pkg/util/slice"
	"k8s.io/legacy-cloud-providers/gce"
)

const (
	svcAPIGroup = "v1"
	svcKind     = "Service"
)

// Controller is a private service connect (psc) controller
// It watches
type Controller struct {
	client kubernetes.Interface

	// possibly want to create a PSC specific cloud interface
	cloud              *gce.Cloud
	saClient           serviceattachmentclient.Interface
	svcAttachmentQueue workqueue.RateLimitingInterface

	// possibly want to create PSC specific namer interface
	saNamer             namer.ServiceAttachmentNamer
	svcAttachmentLister cache.Indexer
	serviceLister       cache.Indexer
	recorder            func(string) record.EventRecorder

	hasSynced func() bool
}

func NewController(ctx *context.ControllerContext) *Controller {
	saNamer := namer.NewServiceAttachmentNamer(ctx.ClusterNamer, string(ctx.KubeSystemUID))
	controller := &Controller{
		client:              ctx.KubeClient,
		cloud:               ctx.Cloud,
		saClient:            ctx.SAClient,
		saNamer:             saNamer,
		svcAttachmentQueue:  workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter()),
		svcAttachmentLister: ctx.SAInformer.GetIndexer(),
		serviceLister:       ctx.ServiceInformer.GetIndexer(),
		hasSynced:           ctx.HasSynced,
		recorder:            ctx.Recorder,
	}

	ctx.SAInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.enqueueServiceAttachment,
		UpdateFunc: func(old, cur interface{}) {
			controller.enqueueServiceAttachment(cur)
		},
	})
	return controller
}

func (c *Controller) Run(stopChan <-chan struct{}) {
	wait.PollUntil(5*time.Second, func() (bool, error) {
		klog.V(2).Infof("Waiting for initial sync")
		return c.hasSynced(), nil
	}, stopChan)

	klog.V(2).Infof("Starting private service connect controller")
	defer func() {
		klog.V(2).Infof("Shutting down private service connect controller")
		c.svcAttachmentQueue.ShutDown()
	}()

	go wait.Until(func() {
		processKey := func() {
			key, quit := c.svcAttachmentQueue.Get()
			if quit {
				return
			}
			defer c.svcAttachmentQueue.Done(key)
			err := c.processServiceAttachment(key.(string))
			c.handleErr(err, key)
		}

		for {
			select {
			case <-stopChan:
				return
			default:
				processKey()
			}
		}
	}, time.Second, stopChan)

	go func() {
		time.Sleep(2 * time.Minute)
		wait.Until(c.garbageCollectServiceAttachments, 2*time.Minute, stopChan)
	}()

	<-stopChan
}

func (c *Controller) handleErr(err error, key interface{}) {
	if err == nil {
		c.svcAttachmentQueue.Forget(key)
		return
	}
	eventMsg := fmt.Sprintf("error processing service attachment %q: %q", key, err)
	klog.Errorf(eventMsg)
	if obj, exists, err := c.svcAttachmentLister.GetByKey(key.(string)); err != nil {
		klog.Warningf("failed to retrieve service attachment %q from the store: %q", key.(string), err)
	} else if exists {
		svcAttachment := obj.(*sav1alpha1.ServiceAttachment)
		c.recorder(svcAttachment.Namespace).Eventf(svcAttachment, v1.EventTypeWarning, "ProcessServiceAttachmentFailed", eventMsg)
	}
	c.svcAttachmentQueue.AddRateLimited(key)
}

func (c *Controller) enqueueServiceAttachment(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		klog.Errorf("Failed to generate service attachment key: %q", err)
		return
	}
	c.svcAttachmentQueue.Add(key)
}

func (c *Controller) processServiceAttachment(key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}

	// TODO: how to deal with doesn't exist
	obj, exists, err := c.svcAttachmentLister.GetByKey(key)
	if err != nil {
		return fmt.Errorf("Errored getting service from store: %q", err)
	}

	if !exists {
		// Allow Garbage Collection to Delete Service Attachment
		return nil
	}

	svcAttachment := obj.(*sav1alpha1.ServiceAttachment)
	updatedCR, err := c.ensureSAFinalizer(svcAttachment)
	if err != nil {
		return fmt.Errorf("Errored adding finalizer on ServiceAttachment CR %s/%s: %s", namespace, name, err)
	}

	connPref, err := GetConnectionPreference(updatedCR.Spec.ConnectionPreference)
	if err != nil {
		return fmt.Errorf("Failed processing ServiceAttachment %s/%s: Invalid connection preference: %s", namespace, name, err)
	}

	if err = validateResourceReference(updatedCR.Spec.ResourceReference); err != nil {
		return err
	}

	frURL, err := c.getForwardingRule(namespace, updatedCR.Spec.ResourceReference.Name)
	if err != nil {
		return fmt.Errorf("Failed to find forwarding rule: %q", err)
	}

	subnetURLs, err := c.getSubnetURLs(updatedCR.Spec.NATSubnets)
	if err != nil {
		return fmt.Errorf("Failed to find nat subnets: %q", err)
	}

	saName := c.saNamer.ServiceAttachment(namespace, name, string(updatedCR.UID))
	gceSvcAttachment := &alpha.ServiceAttachment{
		ConnectionPreference:   connPref,
		Name:                   saName,
		NatSubnets:             subnetURLs,
		ProducerForwardingRule: frURL,
		Region:                 c.cloud.Region(),
	}

	gceSAKey, err := composite.CreateKey(c.cloud, saName, meta.Regional)
	if err != nil {
		return fmt.Errorf("Failed to create key for GCE Service Attachment: %q", err)
	}

	existingSA, err := c.cloud.Compute().AlphaServiceAttachments().Get(context2.Background(), gceSAKey)
	if err != nil && !utils.IsHTTPErrorCode(err, http.StatusNotFound) {
		return fmt.Errorf("Failed querying for GCE Service Attachment: %q", err)
	}

	if existingSA != nil {
		err = validateUpdate(existingSA, gceSvcAttachment)
		if err != nil {
			return fmt.Errorf("Invalid Service Attachment Update: %q", err)
		}
		return nil
	}

	return c.cloud.Compute().AlphaServiceAttachments().Insert(context2.Background(), gceSAKey, gceSvcAttachment)
}

func (c *Controller) garbageCollectServiceAttachments() {
	klog.V(2).Infof("Staring Service Attachment Garbage Collection")
	defer klog.V(2).Infof("Finished Service Attachment Garbage Collection")
	crs := c.svcAttachmentLister.List()
	for _, obj := range crs {
		sa := obj.(*sav1alpha1.ServiceAttachment)
		if sa.GetDeletionTimestamp().IsZero() {
			continue
		}
		gceName := c.saNamer.ServiceAttachment(sa.Namespace, sa.Name, string(sa.UID))

		if err := c.ensureDeleteServiceAttachment(gceName); utils.IgnoreHTTPNotFound(err) != nil {
			eventMsg := fmt.Sprintf("Failed to Garbage Collect Service Attachment %s/%s: %q", sa.Namespace, sa.Name, err)
			klog.Errorf(eventMsg)
			c.recorder(sa.Namespace).Eventf(sa, v1.EventTypeWarning, "ServiceAttachmentGCFailed", eventMsg)
			continue
		}

		if err := c.ensureSAFinalizerRemoved(sa); err != nil {
			//handle error situation
		}
	}
}

func (c *Controller) ensureDeleteServiceAttachment(name string) error {
	saKey, err := composite.CreateKey(c.cloud, name, meta.Regional)
	if err != nil {
		return fmt.Errorf("failed to create key for service attachment %q", name)
	}
	_, err = c.cloud.Compute().AlphaServiceAttachments().Get(context2.Background(), saKey)
	if err != nil {
		if utils.IsHTTPErrorCode(err, http.StatusNotFound) || utils.IsHTTPErrorCode(err, http.StatusBadRequest) {
			return nil
		}
		return fmt.Errorf("failed querying for service attachment %q: %q", name, err)
	}

	return c.cloud.Compute().AlphaServiceAttachments().Delete(context2.Background(), saKey)
}

func (c *Controller) ensureSAFinalizer(saCR *sav1alpha1.ServiceAttachment) (*sav1alpha1.ServiceAttachment, error) {
	if len(saCR.Finalizers) != 0 {
		for _, finalizer := range saCR.Finalizers {
			if finalizer == common.ServiceAttachmentFinalizerKey {
				return saCR, nil
			}
		}
	}

	updatedCR := saCR.DeepCopy()

	if updatedCR.Finalizers == nil {
		updatedCR.Finalizers = []string{}
	}
	updatedCR.Finalizers = append(updatedCR.Finalizers, common.ServiceAttachmentFinalizerKey)
	patchBytes, err := patch.MergePatchBytes(saCR, updatedCR)
	if err != nil {
		return saCR, err
	}

	return c.saClient.NetworkingV1alpha1().ServiceAttachments(saCR.Namespace).Patch(context2.Background(), updatedCR.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
}

func (c *Controller) ensureSAFinalizerRemoved(cr *sav1alpha1.ServiceAttachment) error {
	updatedCR := cr.DeepCopy()
	updatedCR.Finalizers = slice.RemoveString(updatedCR.Finalizers, common.ServiceAttachmentFinalizerKey, nil)

	patchBytes, err := patch.MergePatchBytes(cr, updatedCR)
	if err != nil {
		return fmt.Errorf("failed to create merge patch for service attachement %s/%s: %q", cr.Namespace, cr.Name, err)
	}

	_, err = c.saClient.NetworkingV1alpha1().ServiceAttachments(cr.Namespace).Patch(context2.Background(), updatedCR.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	return err
}

func (c *Controller) getForwardingRule(namespace, svcName string) (string, error) {

	svcKey := fmt.Sprintf("%s/%s", namespace, svcName)
	obj, exists, err := c.serviceLister.GetByKey(svcKey)
	if err != nil {
		return "", fmt.Errorf("Errored getting service %s/%s: %q", namespace, svcName, err)
	}

	if !exists {
		return "", fmt.Errorf("Failed to get Service %s/%s: %q", namespace, svcName, err)
	}

	svc := obj.(*v1.Service)

	frName, ok := svc.Annotations[annotations.TCPForwardingRuleKey]
	if !ok {
		if frName, ok = svc.Annotations[annotations.UDPForwardingRuleKey]; !ok {
			frName = cloudprovider.DefaultLoadBalancerName(svc)
		}
	}
	fwdRule, err := c.cloud.Compute().ForwardingRules().Get(context2.Background(), meta.RegionalKey(frName, c.cloud.Region()))
	if err != nil {
		return "", fmt.Errorf("Failed to get Forwarding Rule %s: %q", frName, err)
	}

	foundMatchingIP := false
	for _, ing := range svc.Status.LoadBalancer.Ingress {
		if ing.IP == fwdRule.IPAddress {
			foundMatchingIP = true
			break
		}
	}

	if foundMatchingIP {
		return fwdRule.SelfLink, nil
	}
	return "", fmt.Errorf("Forwarding rule does not have matching IPAddr")
}

func (c *Controller) getSubnetURLs(subnets []string) ([]string, error) {

	var subnetURLs []string
	for _, subnetName := range subnets {
		subnet, err := c.cloud.Compute().AlphaSubnetworks().Get(context2.Background(), meta.RegionalKey(subnetName, c.cloud.Region()))
		if err != nil {
			return subnetURLs, fmt.Errorf("Failed to get Subnetwork %s: %q", subnetName, err)
		}
		subnetURLs = append(subnetURLs, subnet.SelfLink)

	}
	return subnetURLs, nil
}

func validateResourceReference(ref sav1alpha1.ResourceReference) error {
	// apiGroup := ""
	// if ref.APIGroup != nil {
	// 	apiGroup = *ref.APIGroup
	// 	// return fmt.Errorf("APIGroup should not be nil")
	// }

	// if apiGroup != svcAPIGroup || ref.Kind != svcKind {
	if ref.Kind != "service" && ref.Kind != "Service" {
		return fmt.Errorf("Invalid resource reference. Only Kind: %s are valid", svcKind)
	}
	return nil
}

func validateUpdate(existingSA, desiredSA *alpha.ServiceAttachment) error {
	if existingSA.ConnectionPreference != desiredSA.ConnectionPreference {
		return fmt.Errorf("ServiceAttachment connection preference cannot be updated from %s to %s", existingSA.ConnectionPreference, desiredSA.ConnectionPreference)
	}

	existingFR, err := cloud.ParseResourceURL(existingSA.ProducerForwardingRule)
	if err != nil {
		return fmt.Errorf("ServiceAttachment existing forwarding rule has malformed URL: %q", err)
	}
	desiredFR, err := cloud.ParseResourceURL(desiredSA.ProducerForwardingRule)
	if err != nil {
		return fmt.Errorf("ServiceAttachment desired forwarding rule has malformed URL: %q", err)
	}
	if !reflect.DeepEqual(existingFR, desiredFR) {
		return fmt.Errorf("ServiceAttachment forwarding rule cannot be updated from %s to %s", existingSA.ProducerForwardingRule, desiredSA.ProducerForwardingRule)
	}

	if len(existingSA.NatSubnets) != len(desiredSA.NatSubnets) {
		return fmt.Errorf("ServiceAttachment NAT Subnets cannot be updated")
	} else {
		subnets := make(map[string]*cloud.ResourceID)
		for _, subnet := range existingSA.NatSubnets {
			existingSN, err := cloud.ParseResourceURL(subnet)
			if err != nil {
				return fmt.Errorf("ServiceAttachment existing subnet has malformed URL: %q", err)
			}
			subnets[existingSN.Key.Name] = existingSN

			for _, subnet := range desiredSA.NatSubnets {
				desiredSN, err := cloud.ParseResourceURL(subnet)
				if err != nil {
					return fmt.Errorf("ServiceAttachment desired subnet has malformed URL: %q", err)
				}

				fmt.Printf("REMOVE ME\nDesiredSN: %+v\nExisting:%+v\n", desiredSN, subnets[desiredSN.Key.Name])

				if !reflect.DeepEqual(subnets[desiredSN.Key.Name], desiredSN) {
					return fmt.Errorf("ServiceAttachment NAT Subnets cannot be updated. Found new subnet: %s", desiredSN.Key.Name)
				}
			}
		}
	}
	return nil
}
