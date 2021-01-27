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
	"k8s.io/ingress-gce/pkg/utils/namer"
	"k8s.io/legacy-cloud-providers/gce"
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
		return err
	}

	if !exists {
		// TODO: delete svc attachment
	}

	svcAttachment := obj.(*sav1alpha1.ServiceAttachment)

	connPref, err := GetConnectionPreference(svcAttachment.Spec.ConnectionPreference)
	if err != nil {
		return fmt.Errorf("Failed processing ServiceAttachment %s/%s: Invalid connection preference: %s", namespace, name, err)
	}

	//TODO make sure reference is a service

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
