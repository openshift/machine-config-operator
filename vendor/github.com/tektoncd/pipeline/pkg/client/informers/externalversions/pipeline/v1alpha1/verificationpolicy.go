/*
Copyright 2020 The Tekton Authors

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

// Code generated by informer-gen. DO NOT EDIT.

package v1alpha1

import (
	"context"
	time "time"

	pipelinev1alpha1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1alpha1"
	versioned "github.com/tektoncd/pipeline/pkg/client/clientset/versioned"
	internalinterfaces "github.com/tektoncd/pipeline/pkg/client/informers/externalversions/internalinterfaces"
	v1alpha1 "github.com/tektoncd/pipeline/pkg/client/listers/pipeline/v1alpha1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
	watch "k8s.io/apimachinery/pkg/watch"
	cache "k8s.io/client-go/tools/cache"
)

// VerificationPolicyInformer provides access to a shared informer and lister for
// VerificationPolicies.
type VerificationPolicyInformer interface {
	Informer() cache.SharedIndexInformer
	Lister() v1alpha1.VerificationPolicyLister
}

type verificationPolicyInformer struct {
	factory          internalinterfaces.SharedInformerFactory
	tweakListOptions internalinterfaces.TweakListOptionsFunc
	namespace        string
}

// NewVerificationPolicyInformer constructs a new informer for VerificationPolicy type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewVerificationPolicyInformer(client versioned.Interface, namespace string, resyncPeriod time.Duration, indexers cache.Indexers) cache.SharedIndexInformer {
	return NewFilteredVerificationPolicyInformer(client, namespace, resyncPeriod, indexers, nil)
}

// NewFilteredVerificationPolicyInformer constructs a new informer for VerificationPolicy type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewFilteredVerificationPolicyInformer(client versioned.Interface, namespace string, resyncPeriod time.Duration, indexers cache.Indexers, tweakListOptions internalinterfaces.TweakListOptionsFunc) cache.SharedIndexInformer {
	return cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options v1.ListOptions) (runtime.Object, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.TektonV1alpha1().VerificationPolicies(namespace).List(context.TODO(), options)
			},
			WatchFunc: func(options v1.ListOptions) (watch.Interface, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.TektonV1alpha1().VerificationPolicies(namespace).Watch(context.TODO(), options)
			},
		},
		&pipelinev1alpha1.VerificationPolicy{},
		resyncPeriod,
		indexers,
	)
}

func (f *verificationPolicyInformer) defaultInformer(client versioned.Interface, resyncPeriod time.Duration) cache.SharedIndexInformer {
	return NewFilteredVerificationPolicyInformer(client, f.namespace, resyncPeriod, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc}, f.tweakListOptions)
}

func (f *verificationPolicyInformer) Informer() cache.SharedIndexInformer {
	return f.factory.InformerFor(&pipelinev1alpha1.VerificationPolicy{}, f.defaultInformer)
}

func (f *verificationPolicyInformer) Lister() v1alpha1.VerificationPolicyLister {
	return v1alpha1.NewVerificationPolicyLister(f.Informer().GetIndexer())
}
