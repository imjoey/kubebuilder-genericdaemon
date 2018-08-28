/*
Copyright 2018 imjoey.

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

package genericdaemon

import (
	"context"
	"fmt"
	"log"
	"reflect"

	mygrouopv1beta1 "github.com/imjoey/genericdaemon/pkg/apis/mygrouop/v1beta1"
	"github.com/nlopes/slack"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// Add creates a new GenericDaemon Controller and adds it to the Manager with default RBAC. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
// USER ACTION REQUIRED: update cmd/manager/main.go to call this mygrouop.Add(mgr) to install this Controller
func Add(mgr manager.Manager) error {
	return add(mgr, newReconciler(mgr))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) reconcile.Reconciler {
	return &ReconcileGenericDaemon{Client: mgr.GetClient(), scheme: mgr.GetScheme()}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("genericdaemon-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to GenericDaemon
	err = c.Watch(&source.Kind{Type: &mygrouopv1beta1.GenericDaemon{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	// Watch a DaemonSet created by GenericDaemon
	err = c.Watch(&source.Kind{Type: &appsv1.DaemonSet{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &mygrouopv1beta1.GenericDaemon{},
	})
	if err != nil {
		return err
	}

	return nil
}

var _ reconcile.Reconciler = &ReconcileGenericDaemon{}

// ReconcileGenericDaemon reconciles a GenericDaemon object
type ReconcileGenericDaemon struct {
	client.Client
	scheme *runtime.Scheme
}

// Reconcile reads that state of the cluster for a GenericDaemon object and makes changes based on the state read
// and what is in the GenericDaemon.Spec
// TODO(user): Modify this Reconcile function to implement your Controller logic.  The scaffolding writes
// a Deployment as an example
// Automatically generate RBAC rules to allow the Controller to read and write Deployments
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mygrouop.mydomain.com,resources=genericdaemons,verbs=get;list;watch;create;update;patch;delete
func (r *ReconcileGenericDaemon) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	// Fetch the GenericDaemon instance
	instance := &mygrouopv1beta1.GenericDaemon{}
	err := r.Get(context.TODO(), request.NamespacedName, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			// Object not found, return.  Created objects are automatically garbage collected.
			// For additional cleanup logic use finalizers.
			notifySlack(instance, "GenericDaemon: "+instance.Name+" Deleted")
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	// Define the desired DaemonSet object
	desiredDaemonset := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name + "-daemonset",
			Namespace: instance.Namespace,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"daemonset": instance.Name + "-daemonset",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"daemonset": instance.Name + "-daemonset",
					},
				},
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{
						"daemon": instance.Spec.Label,
					},
					Containers: []corev1.Container{
						{
							Name:  "genericdaemon",
							Image: instance.Spec.Image,
						},
					},
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(instance, desiredDaemonset, r.scheme); err != nil {
		return reconcile.Result{}, err
	}

	// Check if the Daemonset already exists
	found := &appsv1.DaemonSet{}
	err = r.Get(context.TODO(), types.NamespacedName{
		Name:      desiredDaemonset.Name,
		Namespace: desiredDaemonset.Namespace,
	}, found)
	if err != nil && errors.IsNotFound(err) {
		log.Printf("Creating Daemonset %s/%s\n", desiredDaemonset.Namespace, desiredDaemonset.Name)
		err = r.Create(context.TODO(), desiredDaemonset)
		if err != nil {
			return reconcile.Result{}, err
		}
		notifySlack(instance, "GenericDaemon: "+instance.Name+" Created")
	} else if err != nil {
		return reconcile.Result{}, err
	}

	// Get the number of Ready daemonsets and set the Count status
	if found.Status.NumberReady != instance.Status.Count {
		log.Printf("Updating Status %s/%s\n", instance.Namespace, instance.Name)
		outdatedStatusCount := instance.Status.Count
		instance.Status.Count = found.Status.NumberReady

		err = r.Update(context.TODO(), instance)
		if err != nil {
			return reconcile.Result{}, err
		}
		notifySlack(instance,
			fmt.Sprintf("GenericDaemon (%s) update count status from %d to %d\n",
				instance.Name, outdatedStatusCount, found.Status.NumberReady))
	}

	// Update the found object and write the result back if there are any changes
	if !reflect.DeepEqual(desiredDaemonset.Spec, found.Spec) {
		found.Spec = desiredDaemonset.Spec
		log.Printf("Updating DaemonSet %s/%s\n", desiredDaemonset.Namespace, desiredDaemonset.Name)
		err = r.Update(context.TODO(), found)
		if err != nil {
			return reconcile.Result{}, err
		}
		notifySlack(instance,
			fmt.Sprintf("GenericDaemon (%s) update Spec from %#v to %#v\n",
				instance.Name, found.Spec, desiredDaemonset.Spec))
	}
	return reconcile.Result{}, nil
}

var api = slack.New("xoxb-208571400640-424966614807-FcYXg1qBVYi89NhKh695fkLR")

func notifySlack(obj interface{}, e string) {
	params := slack.PostMessageParameters{}
	attachment := prepareSlackAttachment(e)

	params.Attachments = []slack.Attachment{attachment}
	params.AsUser = true
	channelID, timestamp, err := api.PostMessage("#k8s", "", params)
	if err != nil {
		log.Printf("%s\n", err)
		return
	}

	log.Printf("Message successfully sent to channel %s at %s", channelID, timestamp)
}

func prepareSlackAttachment(e string) slack.Attachment {

	attachment := slack.Attachment{
		Fields: []slack.AttachmentField{
			slack.AttachmentField{
				Title: "kubebuilderslack",
				Value: e,
			},
		},
	}

	attachment.MarkdownIn = []string{"fields"}

	return attachment
}
