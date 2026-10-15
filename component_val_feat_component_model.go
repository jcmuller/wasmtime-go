package wasmtime

// #include "shims.h"
import "C"

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"
)

// TODO: map the remaining component value kinds (bool, the other integers,
// floats, char, and the composite kinds other than list and record) and
// guest-defined resources.

// ComponentResourceType is the type of a component resource. Create a
// host-defined one with [NewComponentResourceTypeHost].
type ComponentResourceType struct {
	_ptr *C.wasmtime_component_resource_type_t
}

// NewComponentResourceTypeHost returns the type of host resources tagged
// with `ty`. Host resources with different tags are different types to a
// guest.
func NewComponentResourceTypeHost(ty uint32) *ComponentResourceType {
	rt := &ComponentResourceType{_ptr: C.wasmtime_component_resource_type_new_host(C.uint32_t(ty))}
	runtime.SetFinalizer(rt, func(rt *ComponentResourceType) {
		rt.Close()
	})
	return rt
}

func (rt *ComponentResourceType) ptr() *C.wasmtime_component_resource_type_t {
	ret := rt._ptr
	if ret == nil {
		panic("object has been closed already")
	}
	maybeGC()
	return ret
}

// Close deallocates this resource type explicitly.
//
// For more information see the documentation for engine.Close().
func (rt *ComponentResourceType) Close() {
	if rt._ptr == nil {
		return
	}
	runtime.SetFinalizer(rt, nil)
	C.wasmtime_component_resource_type_delete(rt._ptr)
	rt._ptr = nil
}

// ComponentResourceHost is a handle to a host-defined resource. Its rep is a
// number whose meaning the host picks, such as an index into a table, and
// its type is the tag given to [NewComponentResourceTypeHost].
type ComponentResourceHost struct {
	_ptr *C.wasmtime_component_resource_host_t
}

// NewComponentResourceHost returns a handle to the host resource `rep` of
// type `ty`, either an `own` handle or a `borrow` handle.
func NewComponentResourceHost(owned bool, rep, ty uint32) *ComponentResourceHost {
	return mkComponentResourceHost(C.wasmtime_component_resource_host_new(C.bool(owned), C.uint32_t(rep), C.uint32_t(ty)))
}

func mkComponentResourceHost(ptr *C.wasmtime_component_resource_host_t) *ComponentResourceHost {
	r := &ComponentResourceHost{_ptr: ptr}
	runtime.SetFinalizer(r, func(r *ComponentResourceHost) {
		r.Close()
	})
	return r
}

func (r *ComponentResourceHost) ptr() *C.wasmtime_component_resource_host_t {
	ret := r._ptr
	if ret == nil {
		panic("object has been closed already")
	}
	maybeGC()
	return ret
}

// Rep returns the host-defined number behind this handle.
func (r *ComponentResourceHost) Rep() uint32 {
	ret := uint32(C.wasmtime_component_resource_host_rep(r.ptr()))
	runtime.KeepAlive(r)
	return ret
}

// Type returns the tag of this resource's type.
func (r *ComponentResourceHost) Type() uint32 {
	ret := uint32(C.wasmtime_component_resource_host_type(r.ptr()))
	runtime.KeepAlive(r)
	return ret
}

// Owned reports whether this is an `own` handle rather than a `borrow`.
func (r *ComponentResourceHost) Owned() bool {
	ret := bool(C.wasmtime_component_resource_host_owned(r.ptr()))
	runtime.KeepAlive(r)
	return ret
}

// Close deallocates this handle explicitly. It does not drop the resource.
//
// For more information see the documentation for engine.Close().
func (r *ComponentResourceHost) Close() {
	if r._ptr == nil {
		return
	}
	runtime.SetFinalizer(r, nil)
	C.wasmtime_component_resource_host_delete(r._ptr)
	r._ptr = nil
}

// mkComponentVal converts `val` to Go without taking ownership of it.
func mkComponentVal(ctx *C.wasmtime_context_t, val *C.wasmtime_component_val_t) (interface{}, error) {
	switch val.kind {
	case C.WASMTIME_COMPONENT_U64:
		return uint64(C.go_wasmtime_component_val_u64_get(val)), nil
	case C.WASMTIME_COMPONENT_STRING:
		return goString(C.go_component_val_string_data(val), C.go_component_val_string_len(val)), nil
	case C.WASMTIME_COMPONENT_LIST:
		list := make([]interface{}, int(C.go_component_val_list_len(val)))
		for i := range list {
			v, err := mkComponentVal(ctx, C.go_component_val_list_at(val, C.size_t(i)))
			if err != nil {
				return nil, err
			}
			list[i] = v
		}
		return list, nil
	case C.WASMTIME_COMPONENT_RECORD:
		record := make(ComponentRecord, int(C.go_component_val_record_len(val)))
		for i := range record {
			v, err := mkComponentVal(ctx, C.go_component_val_record_at(val, C.size_t(i)))
			if err != nil {
				return nil, err
			}
			name := goString(C.go_component_val_record_name_data(val, C.size_t(i)), C.go_component_val_record_name_len(val, C.size_t(i)))
			record[i] = ComponentField{Name: name, Val: v}
		}
		return record, nil
	case C.WASMTIME_COMPONENT_RESOURCE:
		var host *C.wasmtime_component_resource_host_t
		err := C.wasmtime_component_resource_any_to_host(ctx, C.go_wasmtime_component_val_resource_get(val), &host)
		if err != nil {
			return nil, mkError(err)
		}
		return mkComponentResourceHost(host), nil
	default:
		return nil, fmt.Errorf("unsupported component value kind %d", val.kind)
	}
}

// ComponentRecord is a WIT `record` value: its fields in the order the type
// declares them.
type ComponentRecord []ComponentField

// ComponentField is one field of a [ComponentRecord].
type ComponentField struct {
	Name string
	Val  interface{}
}

// setComponentVal writes `v` into `dst`, which the caller must delete with
// `wasmtime_component_val_delete`. On error `dst` is a `bool`, which owns
// nothing.
//
// Each list element and record value starts as a `bool`, which owns nothing,
// so a delete after a partial fill frees only what was written.
func setComponentVal(ctx *C.wasmtime_context_t, dst *C.wasmtime_component_val_t, v interface{}) error {
	switch v := v.(type) {
	case uint64:
		dst.kind = C.WASMTIME_COMPONENT_U64
		C.go_wasmtime_component_val_u64_set(dst, C.uint64_t(v))
	case string:
		C.go_component_val_string_init(dst, cString(v), C.size_t(len(v)))
		runtime.KeepAlive(v)
	case []interface{}:
		C.go_component_val_list_init(dst, C.size_t(len(v)))
		for i, e := range v {
			if err := setComponentVal(ctx, C.go_component_val_list_at(dst, C.size_t(i)), e); err != nil {
				return deleteVec(ctx, dst, i, err)
			}
		}
	case ComponentRecord:
		C.go_component_val_record_init(dst, C.size_t(len(v)))
		for i, f := range v {
			C.go_component_val_record_set_name(dst, C.size_t(i), cString(f.Name), C.size_t(len(f.Name)))
			runtime.KeepAlive(f.Name)
		}
		for i, f := range v {
			if err := setComponentVal(ctx, C.go_component_val_record_at(dst, C.size_t(i)), f.Val); err != nil {
				return deleteVec(ctx, dst, i, err)
			}
		}
	case *ComponentResourceHost:
		if !v.Owned() {
			return errors.New("a host borrow handle exists only inside a call")
		}
		var res *C.wasmtime_component_resource_any_t
		err := C.wasmtime_component_resource_host_to_any(ctx, v.ptr(), &res)
		runtime.KeepAlive(v)
		if err != nil {
			return mkError(err)
		}
		dst.kind = C.WASMTIME_COMPONENT_RESOURCE
		C.go_wasmtime_component_val_resource_set(dst, res)
	default:
		return fmt.Errorf("unsupported component value type %T", v)
	}
	return nil
}

// deleteVec drops the resources in the first `n` elements of the list or
// record in `dst`, frees it, and returns `err`.
func deleteVec(ctx *C.wasmtime_context_t, dst *C.wasmtime_component_val_t, n int, err error) error {
	for j := 0; j < n; j++ {
		elem := C.go_component_val_record_at(dst, C.size_t(j))
		if dst.kind == C.WASMTIME_COMPONENT_LIST {
			elem = C.go_component_val_list_at(dst, C.size_t(j))
		}
		if dropErr := dropResources(ctx, elem); dropErr != nil {
			err = errors.Join(err, dropErr)
			break
		}
	}
	C.go_component_val_vec_delete(dst)
	return err
}

// goString copies the `n` bytes at `data` into a Go string. `data` is nil
// when `n` is 0, as the shims give it.
func goString(data *C.char, n C.size_t) string {
	return string(unsafe.Slice((*byte)(unsafe.Pointer(data)), int(n)))
}

// cString returns the bytes of `s` for a C call that reads `len(s)` bytes, or
// nil for an empty `s`. The caller keeps `s` alive until the call returns.
func cString(s string) *C.char {
	if len(s) == 0 {
		return nil
	}
	return C._GoStringPtr(s)
}
