package wasmtime

// #include "shims.h"
import "C"

import (
	"errors"
	"runtime"
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
//   - `*ComponentResourceHost` for `own` and `borrow` handles to host resources
//
// A handle passed as an argument must be owned. For a `borrow` parameter the
// guest borrows it for the call and Call drops it afterwards; for an `own`
// parameter the guest takes it.
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
		if cargs[i].kind != C.WASMTIME_COMPONENT_RESOURCE {
			continue
		}
		var name *C.char
		var nameLen C.size_t
		var paramType C.wasmtime_component_valtype_t
		if !C.wasmtime_component_func_type_param_nth(ty, C.size_t(i), &name, &nameLen, &paramType) {
			continue
		}
		borrow := paramType.kind == C.WASMTIME_COMPONENT_VALTYPE_BORROW
		C.wasmtime_component_valtype_delete(&paramType)
		if !borrow {
			continue
		}
		if err := C.wasmtime_component_resource_any_drop(ctx, C.go_wasmtime_component_val_resource_get(&cargs[i])); err != nil {
			return mkError(err)
		}
	}
	return nil
}

func componentValPtr(vals []C.wasmtime_component_val_t) *C.wasmtime_component_val_t {
	if len(vals) == 0 {
		return nil
	}
	return &vals[0]
}
