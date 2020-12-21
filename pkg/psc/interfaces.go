/*
Copyright 2018 The Kubernetes Authors.

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

	"github.com/GoogleCloudPlatform/k8s-cloud-provider/pkg/cloud/filter"
	"github.com/GoogleCloudPlatform/k8s-cloud-provider/pkg/cloud/meta"
	alpha "google.golang.org/api/compute/v0.alpha"
)

type ServiceAttachmentCloud interface {
	Get(ctx context.Context, key *meta.Key) (*alpha.ServiceAttachment, error)
	List(ctx context.Context, fl *filter.F) ([]*alpha.ServiceAttachment, error)
	Insert(ctx context.Context, key *meta.Key, obj *alpha.ServiceAttachment) error
	Delete(ctx context.Context, key *meta.Key) error
}
