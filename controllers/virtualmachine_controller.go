/*
Copyright 2021.

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

package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/types"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vmwarev1alpha1 "example.com/vm-operator/api/v1alpha1"
)

const (
	finalizerID       = "vm-operator"
	defaultNameLength = 8 // length of generated names
	defaultRequeue    = 20 * time.Second
	successMessage    = "successfully reconciled Vm"
)

// VirtualMachineReconciler reconciles a VirtualMachine object
type VirtualMachineReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
	Finder *find.Finder
	VC     *govmomi.Client // owns vCenter connection
}

//+kubebuilder:rbac:groups=vmware.example.com,resources=virtualmachines,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=vmware.example.com,resources=virtualmachines/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=vmware.example.com,resources=virtualmachines/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the VirtualMachine object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.8.3/pkg/reconcile
func (r *VirtualMachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("virtualmachine", req.NamespacedName)

	virtualMachine := &vmwarev1alpha1.VirtualMachine{}
	if err := r.Client.Get(ctx, req.NamespacedName, virtualMachine); err != nil {
		// add some debug information if it's not a NotFound error
		if !apierrors.IsNotFound(err) {
			log.Error(err, "unable to fetch vm")
		}
		// we'll ignore not-found errors, since they can't be fixed by an immediate
		// requeue (we'll need to wait for a new notification), and we can get them
		// on deleted requests.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	msg := fmt.Sprintf("received reconcile request for %q (namespace: %q)", virtualMachine.GetName(), virtualMachine.GetNamespace())
	log.Info(msg)

	// is object marked for deletion?
	if !virtualMachine.ObjectMeta.DeletionTimestamp.IsZero() {
		log.Info("Vm marked for deletion")
		// The object is being deleted
		if containsString(virtualMachine.ObjectMeta.Finalizers, finalizerID) {
			// our finalizer is present, so lets handle any external dependency
			if err := r.deleteExternalResources(ctx, r.Finder, virtualMachine); err != nil {
				// if fail to delete the external dependency here, return with error
				// so that it can be retried
				return ctrl.Result{}, err
			}

			// remove our finalizer from the list and update it.
			virtualMachine.ObjectMeta.Finalizers = removeString(virtualMachine.ObjectMeta.Finalizers, finalizerID)
			if err := r.Update(ctx, virtualMachine); err != nil {
				return ctrl.Result{}, errors.Wrap(err, "could not remove finalizer")
			}
		}
		// finalizer already removed, nothing to do
		return ctrl.Result{}, nil
	}

	// register our finalizer if it does not exist
	if !containsString(virtualMachine.ObjectMeta.Finalizers, finalizerID) {
		virtualMachine.ObjectMeta.Finalizers = append(virtualMachine.ObjectMeta.Finalizers, finalizerID)
		if err := r.Update(ctx, virtualMachine); err != nil {
			return ctrl.Result{}, errors.Wrap(err, "could not add finalizer")
		}
	}

	if virtualMachine.Status.Phase == "" {
		virtualMachine.Status = createStatus(vmwarev1alpha1.PendingStatusPhase, "initialized", nil, virtualMachine.Name, nil)
		return ctrl.Result{}, errors.Wrap(r.Client.Status().Update(ctx, virtualMachine), "could not update status")
	}

	// Check if VM exists.
	vmRef, err := findVM(ctx, virtualMachine, log, r.VC)
	if err != nil {
		if !IsNotFound(err) {
			return ctrl.Result{}, err
		} else {
			// Create the VM if not found in vCenter.
			return r.reconcileCreateVM(ctx, r.Finder, req.Name, *virtualMachine)
		}
	}

	// VM exists in vCenter. See if it needs to be reconfigured.
	return r.reconcileVMProperties(ctx, vmRef, *virtualMachine)
}

// SetupWithManager sets up the controller with the Manager.
func (r *VirtualMachineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&vmwarev1alpha1.VirtualMachine{}).
		Complete(r)
}

func (r *VirtualMachineReconciler) reconcileCreateVM(
	ctx context.Context,
	finder *find.Finder,
	name string,
	virtualMachine vmwarev1alpha1.VirtualMachine) (ctrl.Result, error) {
	r.Log.Info("creating VM")
	err := createVM(ctx, finder, name, virtualMachine)
	if err != nil {
		msg := "could not create VM in vCenter"
		virtualMachine.Status = createStatus(vmwarev1alpha1.ErrorStatusPhase, msg, err, virtualMachine.Name, nil)
		return ctrl.Result{}, errors.Wrap(r.Client.Status().Update(ctx, &virtualMachine), "could not update status")
	}

	vmRef, _ := findVM(ctx, &virtualMachine, r.Log, r.VC)
	virtualMachine.Status = createStatus(vmwarev1alpha1.RunningStatusPhase, successMessage, nil, virtualMachine.Name, &vmRef.Value)
	return ctrl.Result{}, errors.Wrap(r.Client.Status().Update(ctx, &virtualMachine), "could not update status")
}

func (r *VirtualMachineReconciler) reconcileVMProperties(
	ctx context.Context,
	vmRef types.ManagedObjectReference,
	virtualMachine vmwarev1alpha1.VirtualMachine) (ctrl.Result, error) {

	// Check if VM needs to be reconfigured.
	r.Log.Info("reconfiguring VM if needed")
	err := reconfigureVM(ctx, r.VC, vmRef, &virtualMachine)
	if err != nil {
		msg := "could not reconfigure VM in vCenter"
		virtualMachine.Status = createStatus(vmwarev1alpha1.ErrorStatusPhase, msg, err, virtualMachine.Name, nil)
		return ctrl.Result{}, errors.Wrap(r.Client.Status().Update(ctx, &virtualMachine), "could not update status")
	}

	// Check if VM needs to be powered on.
	err = powerOnVM(ctx, r.VC, vmRef, r.Log)
	if err != nil {
		msg := "could not power on VM in vCenter"
		virtualMachine.Status = createStatus(vmwarev1alpha1.ErrorStatusPhase, msg, err, virtualMachine.Name, nil)
		return ctrl.Result{}, errors.Wrap(r.Client.Status().Update(ctx, &virtualMachine), "could not update status")
	}

	virtualMachine.Status = createStatus(vmwarev1alpha1.RunningStatusPhase, successMessage, nil, virtualMachine.Name, &vmRef.Value)
	return ctrl.Result{}, nil
}

func reconfigureVM(ctx context.Context, VC *govmomi.Client, vmRef types.ManagedObjectReference, vm *vmwarev1alpha1.VirtualMachine) error {
	obj := object.NewVirtualMachine(VC.Client, vmRef)

	spec := types.VirtualMachineConfigSpec{
		NumCPUs:  vm.Spec.CPU,
		MemoryMB: int64(1024 * vm.Spec.Memory),
	}
	task, err := obj.Reconfigure(ctx, spec)
	if err != nil {
		return errors.Wrap(err, "could not initiate reconfigure task")
	}

	err = task.Wait(ctx)
	if err != nil {
		return errors.Wrapf(err, "could not reconfigure %q", obj.Name())
	}

	return nil
}

func powerOnVM(ctx context.Context, VC *govmomi.Client, vmRef types.ManagedObjectReference, logger logr.Logger) error {
	obj := object.NewVirtualMachine(VC.Client, vmRef)
	powerState, err := obj.PowerState(ctx)
	if err != nil {
		return err
	}
	if powerState != types.VirtualMachinePowerStatePoweredOn {
		logger.Info("powering on the vm")
		task, err := obj.PowerOn(ctx)
		if err != nil {
			return errors.Wrap(err, "could not initiate power on task")
		}

		err = task.Wait(ctx)
		if err != nil {
			return errors.Wrapf(err, "could not power on %q", obj.Name())
		}
	}

	logger.Info("vm is in powered on state")

	return nil
}

func powerOffVM(ctx context.Context, VC *govmomi.Client, vmRef types.ManagedObjectReference) error {
	obj := object.NewVirtualMachine(VC.Client, vmRef)
	powerState, err := obj.PowerState(ctx)
	if err != nil {
		return err
	}

	if powerState != types.VirtualMachinePowerStatePoweredOff {
		task, err := obj.PowerOff(ctx)
		if err != nil {
			return errors.Wrap(err, "could not initiate power off task")
		}

		err = task.Wait(ctx)
		if err != nil {
			return errors.Wrapf(err, "could not power off %q", obj.Name())
		}
	}

	return nil
}

func destroyVM(ctx context.Context, VC *govmomi.Client, vmRef types.ManagedObjectReference) error {
	obj := object.NewVirtualMachine(VC.Client, vmRef)

	task, err := obj.Destroy(ctx)
	if err != nil {
		return errors.Wrap(err, "could not initiate destroy VM task")
	}

	err = task.Wait(ctx)
	if err != nil {
		return errors.Wrapf(err, "could not destroy %q", obj.Name())
	}

	return nil
}

func createVM(
	ctx context.Context,
	finder *find.Finder,
	name string,
	virtualMachine vmwarev1alpha1.VirtualMachine) error {
	tmpl, err := finder.VirtualMachine(ctx, virtualMachine.Spec.Template)
	if err != nil {
		return errors.Wrap(err, "could not find template")
	}

	folder, err := finder.DefaultFolder(ctx)
	if err != nil {
		return errors.Wrap(err, "could not find default folder")
	}

	pool, err := finder.ResourcePool(ctx, virtualMachine.Spec.ResourcePool)
	if err != nil {
		return errors.Wrap(err, "could not find default resource pool")
	}

	rpRef := pool.Reference()
	cs := types.VirtualMachineCloneSpec{
		Location: types.VirtualMachineRelocateSpec{
			Pool: &rpRef,
		},
		Config: &types.VirtualMachineConfigSpec{
			NumCPUs:      virtualMachine.Spec.CPU,
			MemoryMB:     int64(1024 * virtualMachine.Spec.Memory),
			InstanceUuid: string(virtualMachine.UID),
		},
		PowerOn: true,
	}

	task, err := tmpl.Clone(ctx, folder, name, cs)
	if err != nil {
		return errors.Wrap(err, "could not initiate clone task")
	}

	err = task.Wait(ctx)
	if err != nil {
		return errors.Wrapf(err, "could not create clone %q", name)
	}

	return nil
}

// ErrNotFound is returned by the findVM function when a VM is not found.
type ErrNotFound struct {
	UUID string
}

func (e ErrNotFound) Error() string {
	return fmt.Sprintf("vm with instance uuid %s not found", e.UUID)
}

// IsNotFound checks if the error is util.ErrNotFound.
func IsNotFound(err error) bool {
	switch err.(type) {
	case ErrNotFound, *ErrNotFound:
		return true
	default:
		return false
	}
}

// findVM searches for a VM in vCenter.
// the VM is queried by its instance UUID.
func findVM(
	ctx context.Context,
	vm *vmwarev1alpha1.VirtualMachine,
	logger logr.Logger,
	VC *govmomi.Client) (types.ManagedObjectReference, error) {
	logger.Info(fmt.Sprintf("Searching for VM with InstanceUUID: %q", vm.UID))
	objRef, err := findByInstanceUUID(ctx, VC, string(vm.UID))
	if err != nil {
		return types.ManagedObjectReference{}, err
	}
	if objRef == nil {
		return types.ManagedObjectReference{}, ErrNotFound{UUID: string(vm.UID)}
	}
	return objRef.Reference(), nil
}

func findByInstanceUUID(ctx context.Context, VC *govmomi.Client, uuid string) (object.Reference, error) {
	si := object.NewSearchIndex(VC.Client)
	findByInstanceUUID := true
	ref, err := si.FindByUuid(ctx, nil, uuid, true, &findByInstanceUUID)
	if err != nil {
		return nil, errors.Wrapf(err, "error finding object by uuid %q", uuid)
	}
	return ref, nil
}

// delete any external resources associated with the Vm
// Ensure that delete implementation is idempotent and safe to invoke
// multiple times for same object.
func (r *VirtualMachineReconciler) deleteExternalResources(ctx context.Context, finder *find.Finder, vm *vmwarev1alpha1.VirtualMachine) error {
	// Check if VM exists.
	vmRef, err := findVM(ctx, vm, r.Log, r.VC)
	if err != nil {
		if !IsNotFound(err) {
			return err
		} else {
			return nil
		}
	}

	r.Log.Info("powering off the vm")
	// Power off the VM and destroy the VM.
	err = powerOffVM(ctx, r.VC, vmRef)
	if err != nil {
		msg := "could not power off VM in vCenter"
		vm.Status = createStatus(vmwarev1alpha1.ErrorStatusPhase, msg, err, vm.Name, nil)
		return errors.Wrap(r.Client.Status().Update(ctx, vm), "could not update status")
	}

	r.Log.Info("destroying the vm")
	err = destroyVM(ctx, r.VC, vmRef)
	if err != nil {
		msg := "could not destroy VM in vCenter"
		vm.Status = createStatus(vmwarev1alpha1.ErrorStatusPhase, msg, err, vm.Name, nil)
		return errors.Wrap(r.Client.Status().Update(ctx, vm), "could not update status")
	}
	r.Log.Info("vm deleted successfully")

	vm.Status = createStatus(vmwarev1alpha1.RunningStatusPhase, successMessage, nil, vm.Name, &vmRef.Value)
	return errors.Wrap(r.Client.Status().Update(ctx, vm), "could not update status")
}

// Helper functions to check and remove string from a slice of strings.
func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

func removeString(slice []string, s string) (result []string) {
	for _, item := range slice {
		if item == s {
			continue
		}
		result = append(result, item)
	}
	return
}

func createStatus(
	phase vmwarev1alpha1.StatusPhase,
	msg string,
	err error,
	name string,
	moRefID *string) vmwarev1alpha1.VirtualMachineStatus {
	if err != nil {
		msg = msg + ": " + err.Error()
	}

	status := vmwarev1alpha1.VirtualMachineStatus{
		Phase:       phase,
		Name:        name,
		LastMessage: msg,
	}

	if moRefID != nil {
		status.MoRefID = *moRefID
	}
	return status
}
