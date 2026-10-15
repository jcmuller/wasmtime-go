package wasmtime

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// atDepth calls f on a new goroutine at stack depth d, so that a stack copy
// lands at a different point of f for each d.
func atDepth(d int, f func()) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		down(d, f)
	}()
	wg.Wait()
}

func down(d int, f func()) int {
	var pad [16]byte
	if d == 0 {
		f()
		return int(pad[0])
	}
	return down(d-1, f) + int(pad[d%16])
}

// eachDepth calls f at each stack depth from 0 to 2999. An empty vector from
// wasmtime has a data pointer such as 0x8, and the Go runtime stops at a
// stack copy that finds such a value in a pointer slot.
func eachDepth(f func()) {
	for d := 0; d < 3000; d++ {
		atDepth(d, f)
	}
}

func TestComponentFuncEmptyListArgAtEachStackDepth(t *testing.T) {
	store, fn := instantiateBorrow(t, "", `(param "l" (list u64))`, "(param i32 i32)", "", nil)

	eachDepth(func() {
		_, err := fn.Call(store, []interface{}{})
		require.NoError(t, err)
	})
}

func TestComponentFuncEmptyStringAtEachStackDepth(t *testing.T) {
	store, fn := instantiateEcho(t, "string", echoArg)

	eachDepth(func() {
		result, err := fn.Call(store, "")
		require.NoError(t, err)
		require.Equal(t, "", result)
	})
}

func TestComponentFuncEmptyListResultAtEachStackDepth(t *testing.T) {
	store, fn := instantiateEcho(t, "(list $point)", echoArg)

	eachDepth(func() {
		result, err := fn.Call(store, []interface{}{})
		require.NoError(t, err)
		require.Equal(t, []interface{}{}, result)
	})
}
