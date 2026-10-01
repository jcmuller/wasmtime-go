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
		s := C.go_wasmtime_component_val_string_get(val)
		return string(unsafe.Slice((*byte)(unsafe.Pointer(s.data)), int(s.size))), nil
	case C.WASMTIME_COMPONENT_LIST:
		l := C.go_wasmtime_component_val_list_get(val)
		elems := unsafe.Slice(l.data, int(l.size))
		list := make([]interface{}, len(elems))
		for i := range elems {
			v, err := mkComponentVal(ctx, &elems[i])
			if err != nil {
				return nil, err
			}
			list[i] = v
		}
		return list, nil
	case C.WASMTIME_COMPONENT_RECORD:
		r := C.go_wasmtime_component_val_record_get(val)
		entries := unsafe.Slice(r.data, int(r.size))
		record := make(ComponentRecord, len(entries))
		for i := range entries {
			v, err := mkComponentVal(ctx, &entries[i].val)
			if err != nil {
				return nil, err
			}
			name := entries[i].name
			record[i] = ComponentField{Name: string(unsafe.Slice((*byte)(unsafe.Pointer(name.data)), int(name.size))), Val: v}
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
// `wasmtime_component_val_delete`. On error `dst` is unchanged and owns
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
		dst.kind = C.WASMTIME_COMPONENT_STRING
		C.go_wasmtime_component_val_string_set(dst, stringToByteVec(v))
	case []interface{}:
		var l C.wasmtime_component_vallist_t
		C.wasmtime_component_vallist_new_uninit(&l, C.size_t(len(v)))
		elems := unsafe.Slice(l.data, len(v))
		for i := range elems {
			elems[i].kind = C.WASMTIME_COMPONENT_BOOL
		}
		for i, e := range v {
			if err := setComponentVal(ctx, &elems[i], e); err != nil {
				C.wasmtime_component_vallist_delete(&l)
				return err
			}
		}
		dst.kind = C.WASMTIME_COMPONENT_LIST
		C.go_wasmtime_component_val_list_set(dst, l)
	case ComponentRecord:
		var r C.wasmtime_component_valrecord_t
		C.wasmtime_component_valrecord_new_uninit(&r, C.size_t(len(v)))
		entries := unsafe.Slice(r.data, len(v))
		for i := range entries {
			entries[i].name = stringToByteVec(v[i].Name)
			entries[i].val.kind = C.WASMTIME_COMPONENT_BOOL
		}
		for i, f := range v {
			if err := setComponentVal(ctx, &entries[i].val, f.Val); err != nil {
				C.wasmtime_component_valrecord_delete(&r)
				return err
			}
		}
		dst.kind = C.WASMTIME_COMPONENT_RECORD
		C.go_wasmtime_component_val_record_set(dst, r)
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
