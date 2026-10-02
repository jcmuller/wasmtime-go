package wasmtime

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// addOneWat imports `add-one` from the host and exports `run`, which calls it.
const addOneWat = `(component
  (import "test:host/math" (instance $math
    (export "add-one" (func (param "x" u64) (result u64)))))
  (alias export $math "add-one" (func $add-one))
  (core func $add-one-core (canon lower (func $add-one)))
  (core module $m
    (import "host" "add-one" (func $add (param i64) (result i64)))
    (func (export "run") (param i64) (result i64)
      local.get 0
      call $add))
  (core instance $host (export "add-one" (func $add-one-core)))
  (core instance $i (instantiate $m (with "host" (instance $host))))
  (func (export "run") (param "x" u64) (result u64)
    (canon lift (core func $i "run"))))`

// defineFunc defines `name` in the instance `instance` of a new linker.
func defineFunc(t *testing.T, engine *Engine, instance, name string, f func([]interface{}) (interface{}, error)) *ComponentLinker {
	t.Helper()
	linker := NewComponentLinker(engine)
	root := linker.Root()
	li, err := root.AddInstance(instance)
	require.NoError(t, err)
	require.NoError(t, li.AddFunc(name, f))
	li.Close()
	root.Close()
	return linker
}

// instantiateRun instantiates addOneWat and returns its `run` export.
func instantiateRun(t *testing.T, f func([]interface{}) (interface{}, error)) (*Store, *ComponentFunc) {
	t.Helper()
	engine := newComponentEngine()
	store := NewStore(engine)
	component := newComponent(t, engine, addOneWat)
	defer component.Close()

	linker := defineFunc(t, engine, "test:host/math", "add-one", f)
	defer linker.Close()

	instance, err := linker.Instantiate(store, component)
	require.NoError(t, err)

	idx := instance.GetExportIndex(store, nil, "run")
	require.NotNil(t, idx)
	defer idx.Close()

	fn := instance.GetFunc(store, idx)
	require.NotNil(t, fn)
	return store, fn
}

func TestComponentLinkerInstanceAddFunc(t *testing.T) {
	store, fn := instantiateRun(t, func(args []interface{}) (interface{}, error) {
		return args[0].(uint64) + 1, nil
	})

	result, err := fn.Call(store, uint64(41))
	require.NoError(t, err)
	require.Equal(t, uint64(42), result)
}

func TestComponentLinkerLockedByOpenInstance(t *testing.T) {
	engine := newComponentEngine()
	linker := NewComponentLinker(engine)
	root := linker.Root()

	require.Panics(t, func() { linker.Root() })

	li, err := root.AddInstance("test:host/math")
	require.NoError(t, err)
	require.Panics(t, func() { _, _ = root.AddInstance("test:host/other") })

	li.Close()
	root.Close()
	linker.Close()
}

func TestComponentFuncHostErrorTraps(t *testing.T) {
	store, fn := instantiateRun(t, func([]interface{}) (interface{}, error) {
		return nil, errors.New("host said no")
	})

	_, err := fn.Call(store, uint64(1))
	require.ErrorContains(t, err, "host said no")
}

func TestComponentFuncHostPanicResumesInCall(t *testing.T) {
	store, fn := instantiateRun(t, func([]interface{}) (interface{}, error) {
		panic("boom")
	})

	require.PanicsWithValue(t, "boom", func() { _, _ = fn.Call(store, uint64(1)) })
}

func TestComponentFuncHostWrongResult(t *testing.T) {
	store, fn := instantiateRun(t, func([]interface{}) (interface{}, error) {
		return nil, nil
	})

	_, err := fn.Call(store, uint64(1))
	require.ErrorContains(t, err, "callback result does not match the function type")
}

func TestComponentFuncCallArgErrors(t *testing.T) {
	store, fn := instantiateRun(t, func(args []interface{}) (interface{}, error) {
		return args[0], nil
	})

	_, err := fn.Call(store)
	require.ErrorContains(t, err, "wrong number of arguments")

	_, err = fn.Call(store, 1)
	require.ErrorContains(t, err, "unsupported component value type int")
}

// echoWat imports `echo` of type `%[1]s -> %[1]s` from the host and exports
// `run` of the same type, which calls it. A bump allocator serves realloc.
const echoWat = `(component
  (import "test:host/echo" (instance $h
    (type $point' (record (field "x" u64) (field "y" u64)))
    (export "point" (type $point (eq $point')))
    (export "echo" (func (param "v" %[1]s) (result %[1]s)))))
  (alias export $h "point" (type $point))
  (alias export $h "echo" (func $echo))
  (core module $libc
    (memory (export "mem") 1)
    (global $next (mut i32) (i32.const 2048))
    (func (export "realloc") (param i32 i32 i32 i32) (result i32)
      (local $p i32)
      global.get $next
      i32.const 7
      i32.add
      i32.const -8
      i32.and
      local.tee $p
      local.get 3
      i32.add
      global.set $next
      local.get $p))
  (core instance $libc (instantiate $libc))
  (alias core export $libc "mem" (core memory $mem))
  (alias core export $libc "realloc" (core func $realloc))
  (core func $echo-core (canon lower (func $echo) (memory $mem) (realloc $realloc)))
  (core module $m
    (import "host" "echo" (func $echo (param i32 i32 i32)))
    (func (export "run") (param i32 i32) (result i32)
      local.get 0
      local.get 1
      i32.const 1024
      call $echo
      i32.const 1024))
  (core instance $hi (export "echo" (func $echo-core)))
  (core instance $i (instantiate $m (with "host" (instance $hi))))
  (func (export "run") (param "v" %[1]s) (result %[1]s)
    (canon lift (core func $i "run") (memory $mem) (realloc $realloc))))`

// instantiateEcho instantiates echoWat for `ty` with `f` behind `echo`.
func instantiateEcho(t *testing.T, ty string, f func([]interface{}) (interface{}, error)) (*Store, *ComponentFunc) {
	t.Helper()
	engine := newComponentEngine()
	store := NewStore(engine)
	component := newComponent(t, engine, fmt.Sprintf(echoWat, ty))
	defer component.Close()

	linker := defineFunc(t, engine, "test:host/echo", "echo", f)
	defer linker.Close()

	instance, err := linker.Instantiate(store, component)
	require.NoError(t, err)

	idx := instance.GetExportIndex(store, nil, "run")
	require.NotNil(t, idx)
	defer idx.Close()

	return store, instance.GetFunc(store, idx)
}

func echoArg(args []interface{}) (interface{}, error) { return args[0], nil }

func TestComponentFuncStringRoundTrip(t *testing.T) {
	store, fn := instantiateEcho(t, "string", echoArg)

	for _, s := range []string{"", "héllo 👩‍👩‍👧"} {
		result, err := fn.Call(store, s)
		require.NoError(t, err)
		require.Equal(t, s, result)
	}
}

func TestComponentFuncListOfRecordsRoundTrip(t *testing.T) {
	store, fn := instantiateEcho(t, "(list $point)", echoArg)

	points := []interface{}{
		ComponentRecord{{Name: "x", Val: uint64(1)}, {Name: "y", Val: uint64(2)}},
		ComponentRecord{{Name: "x", Val: uint64(3)}, {Name: "y", Val: uint64(4)}},
	}
	result, err := fn.Call(store, points)
	require.NoError(t, err)
	require.Equal(t, points, result)

	result, err = fn.Call(store, []interface{}{})
	require.NoError(t, err)
	require.Equal(t, []interface{}{}, result)
}

func TestComponentFuncListWithBadElement(t *testing.T) {
	store, fn := instantiateEcho(t, "(list $point)", echoArg)

	_, err := fn.Call(store, []interface{}{
		ComponentRecord{{Name: "x", Val: uint64(1)}, {Name: "y", Val: uint64(2)}},
		ComponentRecord{{Name: "x", Val: uint64(1)}, {Name: "y", Val: 2}},
	})
	require.ErrorContains(t, err, "unsupported component value type int")
}

func TestComponentFuncCallAfterComponentClose(t *testing.T) {
	engine := newComponentEngine()
	store := NewStore(engine)
	component := newComponent(t, engine, addOneWat)

	linker := defineFunc(t, engine, "test:host/math", "add-one", func(args []interface{}) (interface{}, error) {
		return args[0], nil
	})
	defer linker.Close()

	instance, err := linker.Instantiate(store, component)
	require.NoError(t, err)
	component.Close()

	idx := instance.GetExportIndex(store, nil, "run")
	require.NotNil(t, idx)
	defer idx.Close()

	result, err := instance.GetFunc(store, idx).Call(store, uint64(7))
	require.NoError(t, err)
	require.Equal(t, uint64(7), result)
}

// docWat imports a host resource `doc` with a `len` method and exports
// `measure`, which borrows a doc, calls `len`, and drops the borrow.
const docWat = `(component
  (import "test:host/doc" (instance $host
    (export "doc" (type $doc (sub resource)))
    (export "[method]doc.len" (func (param "self" (borrow $doc)) (result u64)))))
  (alias export $host "doc" (type $doc))
  (alias export $host "[method]doc.len" (func $len))
  (core func $len-core (canon lower (func $len)))
  (core func $drop-core (canon resource.drop $doc))
  (core module $m
    (import "host" "len" (func $len (param i32) (result i64)))
    (import "host" "drop" (func $drop (param i32)))
    (func (export "measure") (param i32) (result i64)
      (local $n i64)
      local.get 0
      call $len
      local.set $n
      local.get 0
      call $drop
      local.get $n))
  (core instance $hi
    (export "len" (func $len-core))
    (export "drop" (func $drop-core)))
  (core instance $i (instantiate $m (with "host" (instance $hi))))
  (func (export "measure") (param "d" (borrow $doc)) (result u64)
    (canon lift (core func $i "measure"))))`

func TestComponentFuncHostResource(t *testing.T) {
	engine := newComponentEngine()
	store := NewStore(engine)
	component := newComponent(t, engine, docWat)
	defer component.Close()

	var self *ComponentResourceHost
	linker := NewComponentLinker(engine)
	defer linker.Close()
	root := linker.Root()
	li, err := root.AddInstance("test:host/doc")
	require.NoError(t, err)
	var dropped []uint32
	require.NoError(t, li.AddResource("doc", NewComponentResourceTypeHost(7), func(rep uint32) error {
		dropped = append(dropped, rep)
		return nil
	}))
	require.NoError(t, li.AddFunc("[method]doc.len", func(args []interface{}) (interface{}, error) {
		self = args[0].(*ComponentResourceHost)
		return uint64(10), nil
	}))
	li.Close()
	root.Close()

	instance, err := linker.Instantiate(store, component)
	require.NoError(t, err)
	idx := instance.GetExportIndex(store, nil, "measure")
	require.NotNil(t, idx)
	defer idx.Close()
	fn := instance.GetFunc(store, idx)

	for i := 0; i < 2; i++ {
		result, err := fn.Call(store, NewComponentResourceHost(true, 3, 7))
		require.NoError(t, err)
		require.Equal(t, uint64(10), result)
	}
	require.Equal(t, uint32(3), self.Rep())
	require.Equal(t, uint32(7), self.Type())
	require.False(t, self.Owned())
	// Call drops a lent handle as a borrow, which runs no destructor.
	require.Empty(t, dropped)

	_, err = fn.Call(store, NewComponentResourceHost(false, 3, 7))
	require.ErrorContains(t, err, "borrow handle exists only inside a call")
}
