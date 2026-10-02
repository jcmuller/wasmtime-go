package wasmtime

// #include "shims.h"
// #include <stdlib.h>
import "C"

import (
	"errors"
	"runtime"
	"sync"
	"unsafe"
)

// ComponentLinkerInstance is an instance being defined inside a
// [ComponentLinker]. Get the root one with [ComponentLinker.Root] and nested
// ones with [ComponentLinkerInstance.AddInstance].
//
// An open instance locks its parent, so close it once its definitions are
// added.
type ComponentLinkerInstance struct {
	_ptr   *C.wasmtime_component_linker_instance_t
	locked bool
	unlock func()
}

func mkComponentLinkerInstance(ptr *C.wasmtime_component_linker_instance_t, unlock func()) *ComponentLinkerInstance {
	li := &ComponentLinkerInstance{_ptr: ptr, unlock: unlock}
	runtime.SetFinalizer(li, func(li *ComponentLinkerInstance) {
		li.Close()
	})
	return li
}

func (li *ComponentLinkerInstance) ptr() *C.wasmtime_component_linker_instance_t {
	ret := li._ptr
	if ret == nil {
		panic("object has been closed already")
	}
	if li.locked {
		panic("linker instance is locked by an open nested ComponentLinkerInstance")
	}
	maybeGC()
	return ret
}

// AddInstance defines a nested instance called `name`, such as
// "wasi:cli/environment@0.2.0", and returns it.
//
// This instance is locked until the returned instance is closed.
func (li *ComponentLinkerInstance) AddInstance(name string) (*ComponentLinkerInstance, error) {
	var ptr *C.wasmtime_component_linker_instance_t
	err := C.wasmtime_component_linker_instance_add_instance(
		li.ptr(),
		C._GoStringPtr(name),
		C._GoStringLen(name),
		&ptr,
	)
	runtime.KeepAlive(li)
	runtime.KeepAlive(name)
	if err != nil {
		return nil, mkError(err)
	}
	li.locked = true
	return mkComponentLinkerInstance(ptr, func() { li.locked = false }), nil
}

// AddFunc defines the function `name` in this instance as `f`.
//
// The arguments that `f` receives follow the types the guest declared for
// the import, so `f` must check them. See [ComponentFunc.Call] for the Go
// type of each value. `f` returns nil for a function without a result.
//
// If `f` returns an error, the guest traps with its message. If `f` panics,
// the guest traps and the panic resumes in Go at the call that entered the
// guest.
func (li *ComponentLinkerInstance) AddFunc(name string, f func(args []interface{}) (interface{}, error)) error {
	idx := insertComponentFunc(f)
	err := C.go_component_linker_instance_add_func(
		li.ptr(),
		C._GoStringPtr(name),
		C._GoStringLen(name),
		C.size_t(idx),
	)
	runtime.KeepAlive(li)
	runtime.KeepAlive(name)
	if err != nil {
		return mkError(err)
	}
	return nil
}

// AddResource defines the resource type `name` in this instance as `ty`.
//
// When a guest drops an owned handle of this type, `drop` runs with the
// handle's rep, so the host can release what the rep points at. The drop of a
// handle that [ComponentFunc.Call] lent runs no `drop`. A nil `drop` does
// nothing. If `drop` returns an error, the drop traps with its message. If
// `drop` panics, the drop traps and the panic resumes in Go at the call that
// entered the guest.
func (li *ComponentLinkerInstance) AddResource(name string, ty *ComponentResourceType, drop func(rep uint32) error) error {
	if drop == nil {
		drop = func(uint32) error { return nil }
	}
	err := C.go_component_linker_instance_add_resource(
		li.ptr(),
		C._GoStringPtr(name),
		C._GoStringLen(name),
		ty.ptr(),
		C.size_t(insertComponentResourceDestructor(drop)),
	)
	runtime.KeepAlive(li)
	runtime.KeepAlive(name)
	runtime.KeepAlive(ty)
	if err != nil {
		return mkError(err)
	}
	return nil
}

// Close deletes this instance and unlocks its parent.
//
// For more information see the documentation for engine.Close().
func (li *ComponentLinkerInstance) Close() {
	if li._ptr == nil {
		return
	}
	if li.locked {
		panic("linker instance is locked by an open nested ComponentLinkerInstance")
	}
	runtime.SetFinalizer(li, nil)
	C.wasmtime_component_linker_instance_delete(li._ptr)
	li._ptr = nil
	li.unlock()
}

var (
	gComponentFuncLock sync.Mutex
	gComponentFunc     = make(map[int]func([]interface{}) (interface{}, error))
	gComponentFuncSlab slab
)

func insertComponentFunc(f func([]interface{}) (interface{}, error)) int {
	gComponentFuncLock.Lock()
	defer gComponentFuncLock.Unlock()
	idx := gComponentFuncSlab.allocate()
	gComponentFunc[idx] = f
	return idx
}

var (
	gComponentResourceDestructorLock sync.Mutex
	gComponentResourceDestructor     = make(map[int]func(uint32) error)
	gComponentResourceDestructorSlab slab
)

func insertComponentResourceDestructor(f func(uint32) error) int {
	gComponentResourceDestructorLock.Lock()
	defer gComponentResourceDestructorLock.Unlock()
	idx := gComponentResourceDestructorSlab.allocate()
	gComponentResourceDestructor[idx] = f
	return idx
}

//export goFinalizeComponentResourceDestructor
func goFinalizeComponentResourceDestructor(env unsafe.Pointer) {
	idx := int(uintptr(env))
	gComponentResourceDestructorLock.Lock()
	defer gComponentResourceDestructorLock.Unlock()
	delete(gComponentResourceDestructor, idx)
	gComponentResourceDestructorSlab.deallocate(idx)
}

//export goComponentResourceDestructor
func goComponentResourceDestructor(ctx *C.wasmtime_context_t, env C.size_t, rep C.uint32_t) *C.wasmtime_error_t {
	gComponentResourceDestructorLock.Lock()
	f := gComponentResourceDestructor[int(env)]
	gComponentResourceDestructorLock.Unlock()

	var err error
	var lastPanic interface{}
	func() {
		defer func() { lastPanic = recover() }()
		err = f(uint32(rep))
	}()
	if lastPanic != nil {
		getDataInContext(ctx).lastPanic = lastPanic
		return newComponentError("go panicked")
	}
	if err != nil {
		return newComponentError(err.Error())
	}
	return nil
}

//export goFinalizeComponentFunc
func goFinalizeComponentFunc(env unsafe.Pointer) {
	idx := int(uintptr(env))
	gComponentFuncLock.Lock()
	defer gComponentFuncLock.Unlock()
	delete(gComponentFunc, idx)
	gComponentFuncSlab.deallocate(idx)
}

//export goTrampolineComponentFunc
func goTrampolineComponentFunc(
	ctx *C.wasmtime_context_t,
	env C.size_t,
	argsPtr *C.wasmtime_component_val_t,
	argsNum C.size_t,
	resultsPtr *C.wasmtime_component_val_t,
	resultsNum C.size_t,
) *C.wasmtime_error_t {
	gComponentFuncLock.Lock()
	f := gComponentFunc[int(env)]
	gComponentFuncLock.Unlock()

	var err error
	var lastPanic interface{}
	func() {
		defer func() { lastPanic = recover() }()
		err = callComponentFunc(ctx, f,
			unsafe.Slice(argsPtr, int(argsNum)),
			unsafe.Slice(resultsPtr, int(resultsNum)))
	}()
	if lastPanic != nil {
		getDataInContext(ctx).lastPanic = lastPanic
		return newComponentError("go panicked")
	}
	if err != nil {
		return newComponentError(err.Error())
	}
	return nil
}

func callComponentFunc(
	ctx *C.wasmtime_context_t,
	f func([]interface{}) (interface{}, error),
	cargs, cresults []C.wasmtime_component_val_t,
) error {
	args := make([]interface{}, len(cargs))
	for i := range cargs {
		arg, err := mkComponentVal(ctx, &cargs[i])
		if err != nil {
			return err
		}
		args[i] = arg
	}

	result, err := f(args)
	if err != nil {
		return err
	}

	switch {
	case len(cresults) == 0 && result == nil:
		return nil
	case len(cresults) == 1 && result != nil:
		return setComponentVal(ctx, &cresults[0], result)
	default:
		return errors.New("callback result does not match the function type")
	}
}

func newComponentError(msg string) *C.wasmtime_error_t {
	cmsg := C.CString(msg)
	defer C.free(unsafe.Pointer(cmsg))
	return C.wasmtime_error_new(cmsg)
}
