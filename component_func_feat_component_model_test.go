package wasmtime

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// borrowWat imports a host resource `doc` with a `len` method and exports
// `run` with the params `%[1]s`. The core func of `run` takes `%[2]s` and runs
// `%[3]s`, with `$len` and `$drop` in scope. `%[4]s` defines the types that
// `%[1]s` names. A bump allocator serves realloc.
const borrowWat = `(component
  (import "test:host/doc" (instance $host
    (export "doc" (type $doc (sub resource)))
    (export "[method]doc.len" (func (param "self" (borrow $doc)) (result u64)))))
  (alias export $host "doc" (type $doc))
  (alias export $host "[method]doc.len" (func $len))
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
  (core func $len-core (canon lower (func $len)))
  (core func $drop-core (canon resource.drop $doc))
  (core module $m
    (import "host" "len" (func $len (param i32) (result i64)))
    (import "host" "drop" (func $drop (param i32)))
    (import "libc" "mem" (memory 1))
    (func (export "run") %[2]s
      %[3]s))
  (core instance $hi
    (export "len" (func $len-core))
    (export "drop" (func $drop-core)))
  (core instance $li (export "mem" (memory $mem)))
  (core instance $i (instantiate $m (with "host" (instance $hi)) (with "libc" (instance $li))))
  %[4]s
  (func (export "run") %[1]s
    (canon lift (core func $i "run") (memory $mem) (realloc $realloc))))`

// instantiateBorrow instantiates borrowWat with `drop` as the destructor of
// `doc` and returns its `run` export.
func instantiateBorrow(t *testing.T, types, params, coreParams, body string, drop func(uint32) error) (*Store, *ComponentFunc) {
	t.Helper()
	engine := newComponentEngine()
	store := NewStore(engine)
	component := newComponent(t, engine, fmt.Sprintf(borrowWat, params, coreParams, body, types))
	defer component.Close()

	linker := NewComponentLinker(engine)
	defer linker.Close()
	root := linker.Root()
	li, err := root.AddInstance("test:host/doc")
	require.NoError(t, err)
	require.NoError(t, li.AddResource("doc", NewComponentResourceTypeHost(7), drop))
	require.NoError(t, li.AddFunc("[method]doc.len", docLen))
	li.Close()
	root.Close()

	instance, err := linker.Instantiate(store, component)
	require.NoError(t, err)
	idx := instance.GetExportIndex(store, nil, "run")
	require.NotNil(t, idx)
	defer idx.Close()
	return store, instance.GetFunc(store, idx)
}

func docLen(args []interface{}) (interface{}, error) { return uint64(10), nil }

func doc() *ComponentResourceHost { return NewComponentResourceHost(true, 3, 7) }

// After a trap wasmtime still counts the borrow it lent, so the handle stays
// in the store until the store closes, and Call returns the trap alone.
func TestComponentFuncTrapWithABorrowReturnsOnlyTheTrap(t *testing.T) {
	store, fn := instantiateBorrow(t, "", `(param "d" (borrow $doc))`, "(param i32)", "unreachable", nil)

	_, err := fn.Call(store, doc())
	require.ErrorContains(t, err, "unreachable")
	require.NotContains(t, err.Error(), "borrowed")
}

func TestComponentFuncDropsTheHandlesOfACallAfterATrap(t *testing.T) {
	store, fn := instantiateBorrow(t, "", `(param "d" (borrow $doc))`, "(param i32)", "unreachable", nil)

	_, err := fn.Call(store, doc())
	require.ErrorContains(t, err, "unreachable")

	_, err = fn.Call(store, doc())
	require.ErrorContains(t, err, "cannot enter component instance")
	require.Equal(t, 1, getDataInStore(store).droppedHandles)
}

func TestComponentFuncDropsABorrowInARecord(t *testing.T) {
	store, fn := instantiateBorrow(t, `(type $r (record (field "d" (borrow $doc))))
  (export $r2 "r" (type $r))`, `(param "r" $r2)`, "(param i32)",
		"local.get 0\n      call $drop", nil)

	_, err := fn.Call(store, ComponentRecord{{Name: "d", Val: doc()}})
	require.NoError(t, err)
	require.Equal(t, 1, getDataInStore(store).droppedHandles)
}

func TestComponentFuncDropsABorrowInAList(t *testing.T) {
	store, fn := instantiateBorrow(t, "", `(param "l" (list (borrow $doc)))`, "(param i32 i32)",
		"local.get 0\n      i32.load\n      call $drop", nil)

	_, err := fn.Call(store, []interface{}{doc()})
	require.NoError(t, err)
	require.Equal(t, 1, getDataInStore(store).droppedHandles)
}

func TestComponentFuncDropsTheHandlesBeforeABadArg(t *testing.T) {
	store, fn := instantiateBorrow(t, "", `(param "d" (borrow $doc)) (param "x" u64)`, "(param i32 i64)", "", nil)

	_, err := fn.Call(store, doc(), 1)
	require.ErrorContains(t, err, "unsupported component value type int")
	require.Equal(t, 1, getDataInStore(store).droppedHandles)
}

func TestComponentFuncDropsTheHandlesBeforeABadElement(t *testing.T) {
	store, fn := instantiateBorrow(t, "", `(param "l" (list (borrow $doc)))`, "(param i32 i32)", "", nil)

	_, err := fn.Call(store, []interface{}{doc(), 1})
	require.ErrorContains(t, err, "unsupported component value type int")
	require.Equal(t, 1, getDataInStore(store).droppedHandles)
}

func TestComponentFuncDropsTheHandlesBeforeABadField(t *testing.T) {
	store, fn := instantiateBorrow(t, `(type $r (record (field "d" (borrow $doc)) (field "x" u64)))
  (export $r2 "r" (type $r))`, `(param "r" $r2)`, "(param i32 i64)", "", nil)

	_, err := fn.Call(store, ComponentRecord{{Name: "d", Val: doc()}, {Name: "x", Val: 1}})
	require.ErrorContains(t, err, "unsupported component value type int")
	require.Equal(t, 1, getDataInStore(store).droppedHandles)
}

func TestComponentFuncRunsTheDestructorWhenTheGuestDropsAnOwnedHandle(t *testing.T) {
	var dropped []uint32
	store, fn := instantiateBorrow(t, "", `(param "d" (own $doc))`, "(param i32)",
		"local.get 0\n      call $drop", func(rep uint32) error {
			dropped = append(dropped, rep)
			return nil
		})

	_, err := fn.Call(store, doc())
	require.NoError(t, err)
	require.Equal(t, []uint32{3}, dropped)
}

func TestComponentFuncTrapsWhenTheDestructorFails(t *testing.T) {
	store, fn := instantiateBorrow(t, "", `(param "d" (own $doc))`, "(param i32)",
		"local.get 0\n      call $drop", func(uint32) error { return errors.New("doc is busy") })

	_, err := fn.Call(store, doc())
	require.ErrorContains(t, err, "doc is busy")
}

func TestComponentFuncPanicsWhenTheDestructorPanics(t *testing.T) {
	store, fn := instantiateBorrow(t, "", `(param "d" (own $doc))`, "(param i32)",
		"local.get 0\n      call $drop", func(uint32) error { panic("host bug") })

	require.PanicsWithValue(t, "host bug", func() { _, _ = fn.Call(store, doc()) })
}
