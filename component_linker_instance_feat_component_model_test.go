package wasmtime

import (
	"errors"
	"fmt"
	"strings"
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
	var rep, ty uint32
	var owned bool
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
		rep, ty, owned = self.Rep(), self.Type(), self.Owned()
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
	require.Equal(t, uint32(3), rep)
	require.Equal(t, uint32(7), ty)
	require.False(t, owned)
	// A borrow handle lives only for the host call that gets it.
	require.PanicsWithValue(t, "object has been closed already", func() { self.Rep() })
	// Call drops a lent handle as a borrow, which runs no destructor.
	require.Empty(t, dropped)

	_, err = fn.Call(store, NewComponentResourceHost(false, 3, 7))
	require.ErrorContains(t, err, "borrow handle exists only inside a call")
}

// TestComponentFuncHostBorrowAllocatesNoHandle: the only allocation of a host
// call that gets a borrow is its args slice, so the borrow allocates no
// ComponentResourceHost. Two guests that call `len` once and twice give the
// cost of one host call. Mallocs counts the whole process, so the test does
// not run in parallel.
func TestComponentFuncHostBorrowAllocatesNoHandle(t *testing.T) {
	callLen := "local.get 0\n      call $len\n      drop\n      "
	allocs := func(calls int) float64 {
		store, fn := instantiateBorrow(t, "", `(param "d" (borrow $doc))`, "(param i32)",
			strings.Repeat(callLen, calls)+"local.get 0\n      call $drop", nil)
		d := doc()
		return testing.AllocsPerRun(100, func() {
			_, err := fn.Call(store, d)
			require.NoError(t, err)
		})
	}

	require.Equal(t, 1.0, allocs(2)-allocs(1))
}

// takeWat imports a host resource `doc` and a host func `take` with the
// params `%[1]s`, after the instance types `%[4]s`. It exports `run`, which
// borrows two docs, runs `%[3]s` to call `take` with the core params
// `%[2]s`, and drops the borrows.
const takeWat = `(component
  (import "test:host/doc" (instance $host
    (export "doc" (type $doc (sub resource)))
    %[4]s
    (export "take" (func %[1]s))))
  (alias export $host "doc" (type $doc))
  (alias export $host "take" (func $take))
  (core module $libc (memory (export "mem") 1))
  (core instance $libc (instantiate $libc))
  (alias core export $libc "mem" (core memory $mem))
  (core func $take-core (canon lower (func $take) (memory $mem)))
  (core func $drop-core (canon resource.drop $doc))
  (core module $m
    (import "host" "take" (func $take %[2]s))
    (import "host" "drop" (func $drop (param i32)))
    (import "libc" "mem" (memory 1))
    (func (export "run") (param i32 i32)
      %[3]s
      local.get 0
      call $drop
      local.get 1
      call $drop))
  (core instance $hi
    (export "take" (func $take-core))
    (export "drop" (func $drop-core)))
  (core instance $li (export "mem" (memory $mem)))
  (core instance $i (instantiate $m (with "host" (instance $hi)) (with "libc" (instance $li))))
  (func (export "run") (param "a" (borrow $doc)) (param "b" (borrow $doc))
    (canon lift (core func $i "run"))))`

// instantiateTake instantiates `wat`, which imports a host resource `doc` and
// a host func `take`, with `take` as that func, and returns its `run` export.
func instantiateTake(t *testing.T, wat string, take func([]interface{}) (interface{}, error)) (*Store, *ComponentFunc) {
	t.Helper()
	engine := newComponentEngine()
	store := NewStore(engine)
	component := newComponent(t, engine, wat)
	defer component.Close()

	linker := NewComponentLinker(engine)
	defer linker.Close()
	root := linker.Root()
	li, err := root.AddInstance("test:host/doc")
	require.NoError(t, err)
	require.NoError(t, li.AddResource("doc", NewComponentResourceTypeHost(7), nil))
	require.NoError(t, li.AddFunc("take", take))
	li.Close()
	root.Close()

	instance, err := linker.Instantiate(store, component)
	require.NoError(t, err)
	idx := instance.GetExportIndex(store, nil, "run")
	require.NotNil(t, idx)
	defer idx.Close()
	return store, instance.GetFunc(store, idx)
}

// TestComponentFuncHostBorrowsAtEachDepth: each borrow of a host call gets
// its own handle, and each one closes when the call returns, at any depth.
func TestComponentFuncHostBorrowsAtEachDepth(t *testing.T) {
	handle := func(v interface{}) *ComponentResourceHost { return v.(*ComponentResourceHost) }
	tests := []struct {
		name, types, params, coreParams, body string
		handles                               func(args []interface{}) []*ComponentResourceHost
	}{
		{
			name:       "two params",
			params:     `(param "a" (borrow $doc)) (param "b" (borrow $doc))`,
			coreParams: "(param i32 i32)",
			body:       "local.get 0\n      local.get 1\n      call $take",
			handles: func(args []interface{}) []*ComponentResourceHost {
				return []*ComponentResourceHost{handle(args[0]), handle(args[1])}
			},
		},
		{
			name:       "list",
			params:     `(param "l" (list (borrow $doc)))`,
			coreParams: "(param i32 i32)",
			body: "i32.const 0\n      local.get 0\n      i32.store\n      " +
				"i32.const 4\n      local.get 1\n      i32.store\n      " +
				"i32.const 0\n      i32.const 2\n      call $take",
			handles: func(args []interface{}) []*ComponentResourceHost {
				l := args[0].([]interface{})
				return []*ComponentResourceHost{handle(l[0]), handle(l[1])}
			},
		},
		{
			name:       "option",
			params:     `(param "a" (borrow $doc)) (param "o" (option (borrow $doc)))`,
			coreParams: "(param i32 i32 i32)",
			body:       "local.get 0\n      i32.const 1\n      local.get 1\n      call $take",
			handles: func(args []interface{}) []*ComponentResourceHost {
				return []*ComponentResourceHost{handle(args[0]), handle(args[1].(ComponentOption).Val)}
			},
		},
		{
			name:       "record",
			types:      `(type $r (record (field "a" (borrow $doc)) (field "b" (borrow $doc)))) (export "r" (type $r2 (eq $r)))`,
			params:     `(param "r" $r2)`,
			coreParams: "(param i32 i32)",
			body:       "local.get 0\n      local.get 1\n      call $take",
			handles: func(args []interface{}) []*ComponentResourceHost {
				r := args[0].(ComponentRecord)
				return []*ComponentResourceHost{handle(r[0].Val), handle(r[1].Val)}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var kept []*ComponentResourceHost
			var reps []uint32
			wat := fmt.Sprintf(takeWat, tt.params, tt.coreParams, tt.body, tt.types)
			store, fn := instantiateTake(t, wat, func(args []interface{}) (interface{}, error) {
				kept = tt.handles(args)
				for _, h := range kept {
					reps = append(reps, h.Rep())
				}
				return nil, nil
			})

			_, err := fn.Call(store, NewComponentResourceHost(true, 3, 7), NewComponentResourceHost(true, 4, 7))
			require.NoError(t, err)
			require.Equal(t, []uint32{3, 4}, reps)
			for _, h := range kept {
				require.PanicsWithValue(t, "object has been closed already", func() { h.Rep() })
			}
		})
	}
}

// TestComponentFuncHostBorrowPutTwiceInArgs: a host func that puts one
// borrow twice in its args does not make a later call share a handle.
func TestComponentFuncHostBorrowPutTwiceInArgs(t *testing.T) {
	var reps []uint32
	calls := 0
	wat := fmt.Sprintf(takeWat, `(param "a" (borrow $doc)) (param "b" (borrow $doc))`, "(param i32 i32)",
		"local.get 0\n      local.get 1\n      call $take", "")
	store, fn := instantiateTake(t, wat, func(args []interface{}) (interface{}, error) {
		calls++
		if calls == 1 {
			args[1] = args[0]
			return nil, nil
		}
		reps = []uint32{args[0].(*ComponentResourceHost).Rep(), args[1].(*ComponentResourceHost).Rep()}
		return nil, nil
	})

	for i := 0; i < 2; i++ {
		_, err := fn.Call(store, NewComponentResourceHost(true, 3, 7), NewComponentResourceHost(true, 4, 7))
		require.NoError(t, err)
	}
	require.Equal(t, []uint32{3, 4}, reps)
}

// ownWat imports a host resource `doc` and a host func `take`, which takes an
// owned doc. It exports `run`, which gives its owned doc to `take`.
const ownWat = `(component
  (import "test:host/doc" (instance $host
    (export "doc" (type $doc (sub resource)))
    (export "take" (func (param "d" (own $doc))))))
  (alias export $host "doc" (type $doc))
  (alias export $host "take" (func $take))
  (core func $take-core (canon lower (func $take)))
  (core module $m
    (import "host" "take" (func $take (param i32)))
    (func (export "run") (param i32)
      local.get 0
      call $take))
  (core instance $hi (export "take" (func $take-core)))
  (core instance $i (instantiate $m (with "host" (instance $hi))))
  (func (export "run") (param "d" (own $doc))
    (canon lift (core func $i "run"))))`

// TestComponentFuncHostOwnOutlivesTheCall: an `own` handle that a host func
// gets belongs to the host, so it lives after the call until the host closes
// it, and each call gets a new one.
func TestComponentFuncHostOwnOutlivesTheCall(t *testing.T) {
	var kept []*ComponentResourceHost
	store, fn := instantiateTake(t, ownWat, func(args []interface{}) (interface{}, error) {
		kept = append(kept, args[0].(*ComponentResourceHost))
		return nil, nil
	})

	for i := 0; i < 2; i++ {
		_, err := fn.Call(store, doc())
		require.NoError(t, err)
	}
	require.NotSame(t, kept[0], kept[1])
	for _, h := range kept {
		require.True(t, h.Owned())
		require.Equal(t, uint32(3), h.Rep())
		require.Equal(t, uint32(7), h.Type())
		h.Close()
		require.PanicsWithValue(t, "object has been closed already", func() { h.Rep() })
	}
}

// u32Wat imports `echo` of type `u32 -> u32` from the host and exports `run`
// of the same type, which calls it. A u32 lowers to one i32, so it needs no
// memory.
const u32Wat = `(component
  (import "test:host/echo" (instance $h
    (export "echo" (func (param "v" u32) (result u32)))))
  (alias export $h "echo" (func $echo))
  (core func $echo-core (canon lower (func $echo)))
  (core module $m
    (import "host" "echo" (func $echo (param i32) (result i32)))
    (func (export "run") (param i32) (result i32)
      local.get 0
      call $echo))
  (core instance $hi (export "echo" (func $echo-core)))
  (core instance $i (instantiate $m (with "host" (instance $hi))))
  (func (export "run") (param "v" u32) (result u32)
    (canon lift (core func $i "run"))))`

// instantiateU32 instantiates u32Wat with `f` behind `echo`.
func instantiateU32(t *testing.T, f func([]interface{}) (interface{}, error)) (*Store, *ComponentFunc) {
	t.Helper()
	engine := newComponentEngine()
	store := NewStore(engine)
	component := newComponent(t, engine, u32Wat)
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

// TestComponentFuncU32RefusesAUint64: only a Go uint32 is a u32, and wasmtime
// checks each value against the function type, so a value past the u32 range
// is an error and never wraps (A1).
func TestComponentFuncU32RefusesAUint64(t *testing.T) {
	store, fn := instantiateU32(t, echoArg)

	_, err := fn.Call(store, uint64(1<<32+7))
	require.ErrorContains(t, err, "type mismatch: expected u32, found u64")

	store, fn = instantiateU32(t, func([]interface{}) (interface{}, error) {
		return uint64(1<<32 + 7), nil
	})

	_, err = fn.Call(store, uint32(7))
	require.ErrorContains(t, err, "type mismatch: expected u32, found u64")
}

func TestComponentFuncOptionRoundTrip(t *testing.T) {
	store, fn := instantiateEcho(t, "(option u32)", echoArg)

	for _, v := range []ComponentOption{{}, {Val: uint32(0)}, {Val: uint32(9)}} {
		result, err := fn.Call(store, v)
		require.NoError(t, err)
		require.Equal(t, v, result)
	}
}

func TestComponentFuncOptionWithABadPayload(t *testing.T) {
	store, fn := instantiateEcho(t, "(option u32)", echoArg)

	_, err := fn.Call(store, ComponentOption{Val: 9})
	require.ErrorContains(t, err, "unsupported component value type int")
}

// findWat exports `find`, which returns `none` for 0 and else a point of x
// and x+1. The result does not fit in one flat value, so `find` returns a
// pointer to it: the case at 0 and the record at 8.
const findWat = `(component
  (core module $m
    (memory (export "mem") 1)
    (func (export "find") (param $x i64) (result i32)
      local.get $x
      i64.eqz
      if
        i32.const 1024
        i32.const 0
        i32.store8
      else
        i32.const 1024
        i32.const 1
        i32.store8
        i32.const 1032
        local.get $x
        i64.store
        i32.const 1040
        local.get $x
        i64.const 1
        i64.add
        i64.store
      end
      i32.const 1024))
  (core instance $i (instantiate $m))
  (alias core export $i "mem" (core memory $mem))
  (type $point' (record (field "x" u64) (field "y" u64)))
  (export $point "point" (type $point'))
  (func (export "find") (param "x" u64) (result (option $point))
    (canon lift (core func $i "find") (memory $mem))))`

func TestComponentFuncReturnsAnOptionOfARecord(t *testing.T) {
	engine := newComponentEngine()
	store := NewStore(engine)
	component := newComponent(t, engine, findWat)
	defer component.Close()

	linker := NewComponentLinker(engine)
	defer linker.Close()
	instance, err := linker.Instantiate(store, component)
	require.NoError(t, err)
	idx := instance.GetExportIndex(store, nil, "find")
	require.NotNil(t, idx)
	defer idx.Close()
	fn := instance.GetFunc(store, idx)

	result, err := fn.Call(store, uint64(0))
	require.NoError(t, err)
	require.Equal(t, ComponentOption{}, result)

	result, err = fn.Call(store, uint64(4))
	require.NoError(t, err)
	require.Equal(t, ComponentOption{Val: ComponentRecord{{Name: "x", Val: uint64(4)}, {Name: "y", Val: uint64(5)}}}, result)
}

func TestComponentFuncU32RoundTrip(t *testing.T) {
	store, fn := instantiateU32(t, echoArg)

	for _, v := range []uint32{0, 7, 1<<32 - 1} {
		result, err := fn.Call(store, v)
		require.NoError(t, err)
		require.Equal(t, v, result)
	}
}
