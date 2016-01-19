// Copyright 2015 Google Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"log"

	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/api/unversioned"
	client "k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/fields"
	"k8s.io/kubernetes/pkg/labels"
)

// ReplicaSetList contains a list of Replica Sets in the cluster.
type ReplicaSetList struct {
	// Unordered list of Replica Sets.
	ReplicaSets []ReplicaSet `json:"replicaSets"`
}

// ReplicaSet (aka. Replication Controller) plus zero or more Kubernetes services that
// target the Replica Set.
type ReplicaSet struct {
	// Name of the Replica Set.
	Name string `json:"name"`

	// Namespace this Replica Set is in.
	Namespace string `json:"namespace"`

	// Human readable description of this Replica Set.
	Description string `json:"description"`

	// Label of this Replica Set.
	Labels map[string]string `json:"labels"`

	// Aggergate information about pods belonging to this repolica set.
	Pods ReplicaSetPodInfo `json:"pods"`

	// Container images of the Replica Set.
	ContainerImages []string `json:"containerImages"`

	// Time the replica set was created.
	CreationTime unversioned.Time `json:"creationTime"`

	// Internal endpoints of all Kubernetes services have the same label selector as this Replica Set.
	InternalEndpoints []Endpoint `json:"internalEndpoints"`

	// External endpoints of all Kubernetes services have the same label selector as this Replica Set.
	ExternalEndpoints []Endpoint `json:"externalEndpoints"`
}

// ReplicaSetPodInfo represents aggregate information about replica set pods.
type ReplicaSetPodInfo struct {
	// Number of pods that are created.
	Curret int `json:"current"`

	// Number of pods that are desired in this Replica Set.
	Desired int `json:"desired"`

	// Number of pods that are currently running.
	Running int `json:"running"`

	// Number of pods that are currently waiting.
	Waiting int `json:"waiting"`

	// Number of pods that are failed.
	Failed int `json:"failed"`
}

// GetReplicaSetList returns a list of all Replica Sets in the cluster.
func GetReplicaSetList(client *client.Client) (*ReplicaSetList, error) {
	log.Printf("Getting list of all replica sets in the cluster")

	listEverything := unversioned.ListOptions{
		LabelSelector: unversioned.LabelSelector{labels.Everything()},
		FieldSelector: unversioned.FieldSelector{fields.Everything()},
	}

	replicaSets, err := client.ReplicationControllers(api.NamespaceAll).List(listEverything)

	if err != nil {
		return nil, err
	}

	services, err := client.Services(api.NamespaceAll).List(listEverything)

	if err != nil {
		return nil, err
	}

	pods, err := client.Pods(api.NamespaceAll).List(listEverything)

	if err != nil {
		return nil, err
	}

	return getReplicaSetList(replicaSets.Items, services.Items, pods.Items), nil
}

// Returns a list of all Replica Set model objects in the cluster, based on all Kubernetes
// Replica Set and Service API objects.
// The function processes all Replica Sets API objects and finds matching Services for them.
func getReplicaSetList(replicaSets []api.ReplicationController, services []api.Service,
	pods []api.Pod) *ReplicaSetList {

	replicaSetList := &ReplicaSetList{ReplicaSets: make([]ReplicaSet, 0)}

	for _, replicaSet := range replicaSets {
		var containerImages []string
		for _, container := range replicaSet.Spec.Template.Spec.Containers {
			containerImages = append(containerImages, container.Image)
		}

		matchingServices := getMatchingServices(services, &replicaSet)
		var internalEndpoints []Endpoint
		var externalEndpoints []Endpoint
		for _, service := range matchingServices {
			internalEndpoints = append(internalEndpoints,
				getInternalEndpoint(service.Name, service.Namespace, service.Spec.Ports))
			for _, externalIP := range service.Status.LoadBalancer.Ingress {
				externalEndpoints = append(externalEndpoints,
					getExternalEndpoint(externalIP, service.Spec.Ports))
			}
		}

		podInfo := getReplicaSetPodInfo(&replicaSet, pods)

		replicaSetList.ReplicaSets = append(replicaSetList.ReplicaSets, ReplicaSet{
			Name:              replicaSet.ObjectMeta.Name,
			Namespace:         replicaSet.ObjectMeta.Namespace,
			Description:       replicaSet.Annotations[DescriptionAnnotationKey],
			Labels:            replicaSet.ObjectMeta.Labels,
			Pods:              podInfo,
			ContainerImages:   containerImages,
			CreationTime:      replicaSet.ObjectMeta.CreationTimestamp,
			InternalEndpoints: internalEndpoints,
			ExternalEndpoints: externalEndpoints,
		})
	}

	return replicaSetList
}

func getReplicaSetPodInfo(replicaSet *api.ReplicationController, pods []api.Pod) ReplicaSetPodInfo {
	result := ReplicaSetPodInfo{
		Curret:  replicaSet.Status.Replicas,
		Desired: replicaSet.Spec.Replicas,
	}

	for _, pod := range pods {
		if pod.ObjectMeta.Namespace == replicaSet.ObjectMeta.Namespace &&
			isLabelSelectorMatching(replicaSet.Spec.Selector, pod.ObjectMeta.Labels) {
			switch pod.Status.Phase {
			case api.PodRunning:
				result.Running++
			case api.PodPending:
				result.Waiting++
			case api.PodFailed:
				result.Failed++
			}
		}
	}

	return result
}

// Returns all services that target the same Pods (or subset) as the given Replica Set.
func getMatchingServices(services []api.Service,
	replicaSet *api.ReplicationController) []api.Service {

	var matchingServices []api.Service
	for _, service := range services {
		if service.ObjectMeta.Namespace == replicaSet.ObjectMeta.Namespace &&
			isLabelSelectorMatching(service.Spec.Selector, replicaSet.Spec.Selector) {

			matchingServices = append(matchingServices, service)
		}
	}
	return matchingServices
}

// Returns true when a Service with the given selector targets the same Pods (or subset) that
// a Replica Set with the given selector.
func isLabelSelectorMatching(labelSelector map[string]string,
	testedObjectLabels map[string]string) bool {

	// If service has no selectors, then assume it targets different Pods.
	if len(labelSelector) == 0 {
		return false
	}
	for label, value := range labelSelector {
		if rsValue, ok := testedObjectLabels[label]; !ok || rsValue != value {
			return false
		}
	}
	return true
}
