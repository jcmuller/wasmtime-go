package wasmtime

// #include "shims.h"
//
// static inline const wasmtime_component_list_type_t *go_component_valtype_list(const wasmtime_component_valtype_t *vt) {
//   return vt->of.list;
// }
//
// static inline const wasmtime_component_record_type_t *go_component_valtype_record(const wasmtime_component_valtype_t *vt) {
//   return vt->of.record;
// }
import "C"

import (
	"errors"
	"runtime"
	"unsafe"
)

// ComponentFunc is a function exported by a [ComponentInstance]. Get one
// with [ComponentInstance.GetFunc].
//
// Like the instance, a function has no destructor; the store that owns the
// instance owns it too.
type ComponentFunc struct {
	val C.wasmtime_component_func_t
}

// GetFunc returns the function behind `idx`, or nil if that export is not a
// function.
func (i *ComponentInstance) GetFunc(store Storelike, idx *ComponentExportIndex) *ComponentFunc {
	var val C.wasmtime_component_func_t
	ok := C.wasmtime_component_instance_get_func(&i.val, store.Context(), idx.ptr(), &val)
	runtime.KeepAlive(i)
	runtime.KeepAlive(store)
	runtime.KeepAlive(idx)
	if !ok {
		return nil
	}
	return &ComponentFunc{val: val}
}

// Call invokes this function with `args` and returns its result, or nil for
// a function without one.
//
// The supported Go types are:
//
//   - `uint64` for `u64`
//   - `string` for `string`
//   - `[]interface{}` for `list`
//   - [ComponentRecord] for `record`
//   - `*ComponentResourceHost` for `own` and `borrow` handles to host resources
//
// A handle passed as an argument must be owned. For a `borrow` parameter, at
// any depth, the guest borrows it for the call and Call drops it afterwards;
// for an `own` parameter the guest takes it. If an argument does not convert,
// Call drops each handle it converted. After a trap, a lent handle stays in
// the store until the store closes.
//
// If a host function called during the call panics, Call panics with the
// same value.
func (f *ComponentFunc) Call(store Storelike, args ...interface{}) (interface{}, error) {
	ctx := store.Context()
	ty := C.wasmtime_component_func_type(&f.val, ctx)
	defer C.wasmtime_component_func_type_delete(ty)

	if len(args) != int(C.wasmtime_component_func_type_param_count(ty)) {
		return nil, errors.New("wrong number of arguments provided")
	}

	cargs := make([]C.wasmtime_component_val_t, len(args))
	converted := 0
	defer func() {
		for i := 0; i < converted; i++ {
			C.wasmtime_component_val_delete(&cargs[i])
		}
	}()
	for i, arg := range args {
		if err := setComponentVal(ctx, &cargs[i], arg); err != nil {
			for j := range cargs[:converted] {
				if dropErr := dropResources(ctx, &cargs[j]); dropErr != nil {
					return nil, errors.Join(err, dropErr)
				}
			}
			return nil, err
		}
		converted++
	}

	var resultType C.wasmtime_component_valtype_t
	hasResult := bool(C.wasmtime_component_func_type_result(ty, &resultType))
	cresults := make([]C.wasmtime_component_val_t, 1)
	nresults := 0
	if hasResult {
		C.wasmtime_component_valtype_delete(&resultType)
		nresults = 1
	}

	err := C.wasmtime_component_func_call(&f.val, ctx,
		componentValPtr(cargs), C.size_t(len(cargs)),
		&cresults[0], C.size_t(nresults))
	runtime.KeepAlive(f)
	runtime.KeepAlive(store)
	runtime.KeepAlive(args)

	data := getDataInStore(store)
	if data.lastPanic != nil {
		lastPanic := data.lastPanic
		data.lastPanic = nil
		if err != nil {
			C.wasmtime_error_delete(err)
		}
		panic(lastPanic)
	}
	// After a trap wasmtime still counts each borrow it lent for the call,
	// so a drop fails, and the handle stays in the store until it closes.
	if err != nil {
		return nil, mkError(err)
	}

	if err := dropBorrowedArgs(ctx, ty, cargs); err != nil {
		return nil, err
	}

	if !hasResult {
		return nil, nil
	}
	defer C.wasmtime_component_val_delete(&cresults[0])
	return mkComponentVal(ctx, &cresults[0])
}

// dropBorrowedArgs drops the host resources that were lent to `borrow`
// parameters, so they leave the store's handle table.
func dropBorrowedArgs(ctx *C.wasmtime_context_t, ty *C.wasmtime_component_func_type_t, cargs []C.wasmtime_component_val_t) error {
	for i := range cargs {
		var name *C.char
		var nameLen C.size_t
		var paramType C.wasmtime_component_valtype_t
		if !C.wasmtime_component_func_type_param_nth(ty, C.size_t(i), &name, &nameLen, &paramType) {
			continue
		}
		err := dropBorrows(ctx, &paramType, &cargs[i])
		C.wasmtime_component_valtype_delete(&paramType)
		if err != nil {
			return err
		}
	}
	return nil
}

// dropBorrows drops each resource in `val` that `ty` marks as `borrow`, at
// any depth.
func dropBorrows(ctx *C.wasmtime_context_t, ty *C.wasmtime_component_valtype_t, val *C.wasmtime_component_val_t) error {
	switch {
	case ty.kind == C.WASMTIME_COMPONENT_VALTYPE_BORROW && val.kind == C.WASMTIME_COMPONENT_RESOURCE:
		return dropResource(ctx, val)
	case ty.kind == C.WASMTIME_COMPONENT_VALTYPE_LIST && val.kind == C.WASMTIME_COMPONENT_LIST:
		var elemType C.wasmtime_component_valtype_t
		C.wasmtime_component_list_type_element(C.go_component_valtype_list(ty), &elemType)
		defer C.wasmtime_component_valtype_delete(&elemType)
		l := C.go_wasmtime_component_val_list_get(val)
		elems := unsafe.Slice(l.data, int(l.size))
		for i := range elems {
			if err := dropBorrows(ctx, &elemType, &elems[i]); err != nil {
				return err
			}
		}
	case ty.kind == C.WASMTIME_COMPONENT_VALTYPE_RECORD && val.kind == C.WASMTIME_COMPONENT_RECORD:
		r := C.go_wasmtime_component_val_record_get(val)
		entries := unsafe.Slice(r.data, int(r.size))
		for i := range entries {
			var name *C.char
			var nameLen C.size_t
			var fieldType C.wasmtime_component_valtype_t
			if !C.wasmtime_component_record_type_field_nth(C.go_component_valtype_record(ty), C.size_t(i), &name, &nameLen, &fieldType) {
				continue
			}
			err := dropBorrows(ctx, &fieldType, &entries[i].val)
			C.wasmtime_component_valtype_delete(&fieldType)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// dropResources drops each resource in `val`, at any depth. Use it only on
// values that never reached a guest.
func dropResources(ctx *C.wasmtime_context_t, val *C.wasmtime_component_val_t) error {
	switch val.kind {
	case C.WASMTIME_COMPONENT_RESOURCE:
		return dropResource(ctx, val)
	case C.WASMTIME_COMPONENT_LIST:
		l := C.go_wasmtime_component_val_list_get(val)
		elems := unsafe.Slice(l.data, int(l.size))
		for i := range elems {
			if err := dropResources(ctx, &elems[i]); err != nil {
				return err
			}
		}
	case C.WASMTIME_COMPONENT_RECORD:
		r := C.go_wasmtime_component_val_record_get(val)
		entries := unsafe.Slice(r.data, int(r.size))
		for i := range entries {
			if err := dropResources(ctx, &entries[i].val); err != nil {
				return err
			}
		}
	}
	return nil
}

// dropResource drops the resource in `val` from the store's handle table.
func dropResource(ctx *C.wasmtime_context_t, val *C.wasmtime_component_val_t) error {
	if err := C.wasmtime_component_resource_any_drop(ctx, C.go_wasmtime_component_val_resource_get(val)); err != nil {
		return mkError(err)
	}
	getDataInContext(ctx).droppedHandles++
	return nil
}

func componentValPtr(vals []C.wasmtime_component_val_t) *C.wasmtime_component_val_t {
	if len(vals) == 0 {
		return nil
	}
	return &vals[0]
}
