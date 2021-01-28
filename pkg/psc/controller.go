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

	"github.com/GoogleCloudPlatform/k8s-cloud-provider/pkg/cloud/meta"
	alpha "google.golang.org/api/compute/v0.alpha"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/ingress-gce/pkg/annotations"
	sav1alpha1 "k8s.io/ingress-gce/pkg/apis/serviceattachment/v1alpha1"
	"k8s.io/ingress-gce/pkg/composite"
	"k8s.io/ingress-gce/pkg/context"
	serviceattachmentclient "k8s.io/ingress-gce/pkg/serviceattachment/client/clientset/versioned"
	"k8s.io/ingress-gce/pkg/utils"
	"k8s.io/ingress-gce/pkg/utils/namer"
	"k8s.io/kubernetes/pkg/apis/core"
	"k8s.io/legacy-cloud-providers/gce"
)

const (
	svcAPIGroup = "v1"
	svcKind     = "service"
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
}

func NewController(ctx *context.ControllerContext) *Controller {
	saNamer := namer.NewServiceAttachmentNamer(ctx.ClusterNamer, string(ctx.KubeSystemUID))
	return &Controller{
		client:              ctx.KubeClient,
		cloud:               ctx.Cloud,
		saClient:            ctx.SAClient,
		saNamer:             saNamer,
		svcAttachmentQueue:  workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter()),
		svcAttachmentLister: ctx.SAInformer.GetIndexer(),
		serviceLister:       ctx.ServiceInformer.GetIndexer(),
	}
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
		// TODO: delete svc attachment
	}

	svcAttachment := obj.(*sav1alpha1.ServiceAttachment)

	connPref, err := GetConnectionPreference(svcAttachment.Spec.ConnectionPreference)
	if err != nil {
		return fmt.Errorf("Failed processing ServiceAttachment %s/%s: Invalid connection preference: %s", namespace, name, err)
	}

	if err = validateResourceReference(svcAttachment.Spec.ResourceReference); err != nil {
		return err
	}

	frURL, err := c.getForwardingRule(namespace, svcAttachment.Spec.ResourceReference.Name)
	if err != nil {
		return fmt.Errorf("Failed to find forwarding rule: %q", err)
	}

	saName := c.saNamer.ServiceAttachment(namespace, name, string(svcAttachment.UID))
	gceSvcAttachment := &alpha.ServiceAttachment{
		ConnectionPreference:   connPref,
		Name:                   saName,
		NatSubnets:             svcAttachment.Spec.NATSubnets,
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
			return "", fmt.Errorf("No Forwarding Rule Annotation exists on service %s/%s", namespace, svcName)
		}
	}
	fwdRule, err := c.cloud.Compute().ForwardingRules().Get(context2.Background(), meta.RegionalKey(frName, c.cloud.Region()))
	if err != nil {
		return "", fmt.Errorf("Failed to get Forwarding Rule %s: %q", frName, err)
	}

	return fwdRule.SelfLink, nil
}

func validateResourceReference(ref *core.TypedLocalObjectReference) error {
	if ref.APIGroup == nil {
		return fmt.Errorf("APIGroup should not be nil")
	}

	if *ref.APIGroup != svcAPIGroup || ref.Kind != svcKind {
		return fmt.Errorf("Invalid resource reference. Only APIGroup: %s and Kind: %s are valid", svcAPIGroup, svcKind)
	}
	return nil
}

func validateUpdate(existingSA, desiredSA *alpha.ServiceAttachment) error {
	if existingSA.ConnectionPreference != desiredSA.ConnectionPreference {
		return fmt.Errorf("ServiceAttachment connection preference cannot be updated from %s", existingSA.ConnectionPreference)
	}

	if existingSA.ProducerForwardingRule != desiredSA.ProducerForwardingRule {
		return fmt.Errorf("ServiceAttachment forwarding rule cannot be updated from %s", existingSA.ProducerForwardingRule)
	}

	if len(existingSA.NatSubnets) != len(desiredSA.NatSubnets) {
		return fmt.Errorf("ServiceAttachment NAT Subnets cannot be updated")
	} else {
		subnets := make(map[string]bool)
		for _, subnet := range existingSA.NatSubnets {
			subnets[subnet] = true
		}

		for _, subnet := range desiredSA.NatSubnets {
			if !subnets[subnet] {
				return fmt.Errorf("ServiceAttachment NAT Subnets cannot be updated. Found new subnet: %s", subnet)
			}
		}
	}
	return nil
}
