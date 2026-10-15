#include <wasm.h>
#include <wasmtime.h>

wasmtime_store_t *go_store_new(wasm_engine_t *engine, size_t env);
void go_func_new(wasmtime_context_t *context, wasm_functype_t *ty, size_t env, int wrap,  wasmtime_func_t *ret);
wasmtime_error_t *go_linker_define_func(
    wasmtime_linker_t *linker,
    const char *module,
    size_t module_len,
    const char *name,
    size_t name_len,
    const wasm_functype_t *ty,
    int wrap,
    size_t env
);

wasmtime_error_t *go_component_linker_instance_add_func(
    wasmtime_component_linker_instance_t *linker_instance,
    const char *name,
    size_t name_len,
    size_t env
);
wasmtime_error_t *go_component_linker_instance_add_resource(
    wasmtime_component_linker_instance_t *linker_instance,
    const char *name,
    size_t name_len,
    const wasmtime_component_resource_type_t *ty,
    size_t env
);

#define EACH_UNION_ACCESSOR(name) \
  UNION_ACCESSOR(wasmtime_val, i32, int32_t) \
  UNION_ACCESSOR(wasmtime_val, i64, int64_t) \
  UNION_ACCESSOR(wasmtime_val, f32, float) \
  UNION_ACCESSOR(wasmtime_val, f64, double) \
  UNION_ACCESSOR(wasmtime_val, funcref, wasmtime_func_t) \
  \
  UNION_ACCESSOR(wasmtime_extern, func, wasmtime_func_t) \
  UNION_ACCESSOR(wasmtime_extern, memory, wasmtime_memory_t) \
  UNION_ACCESSOR(wasmtime_extern, table, wasmtime_table_t) \
  UNION_ACCESSOR(wasmtime_extern, global, wasmtime_global_t) \
  \
  UNION_ACCESSOR(wasmtime_component_val, u64, uint64_t) \
  UNION_ACCESSOR(wasmtime_component_val, resource, wasmtime_component_resource_any_t*)

#define UNION_ACCESSOR(name, field, ty) \
  ty go_##name##_##field##_get(const name##_t *val); \
  void go_##name##_##field##_set(name##_t *val, ty i);

EACH_UNION_ACCESSOR(UNION_ACCESSOR)

#undef UNION_ACCESSOR

// The string, list and record of a component value are vectors, and an empty
// vector from wasmtime has a data pointer such as 0x1 or 0x8. Go must never
// keep such a value in a pointer, because a stack copy then stops the
// process. So Go never gets a vector struct: it reads the length and one
// element at a time, gets NULL for the data of an empty string, and builds
// each vector in place here.
//
// cgo includes this file more than once in one unit, so these definitions
// have a guard.
#ifndef GO_COMPONENT_VAL_VECTORS
#define GO_COMPONENT_VAL_VECTORS

static inline size_t go_component_val_string_len(const wasmtime_component_val_t *val) {
  return val->of.string.size;
}

static inline const char *go_component_val_string_data(const wasmtime_component_val_t *val) {
  return val->of.string.size ? (const char *)val->of.string.data : NULL;
}

// go_component_val_string_init sets `val` to a copy of the `len` bytes at `data`.
static inline void go_component_val_string_init(wasmtime_component_val_t *val, const char *data, size_t len) {
  wasm_byte_vec_new(&val->of.string, len, (const wasm_byte_t *)data);
  val->kind = WASMTIME_COMPONENT_STRING;
}

static inline size_t go_component_val_list_len(const wasmtime_component_val_t *val) {
  return val->of.list.size;
}

static inline wasmtime_component_val_t *go_component_val_list_at(wasmtime_component_val_t *val, size_t i) {
  return &val->of.list.data[i];
}

// go_component_val_list_init sets `val` to a list of `len` bool values, which
// own nothing.
static inline void go_component_val_list_init(wasmtime_component_val_t *val, size_t len) {
  wasmtime_component_vallist_new_uninit(&val->of.list, len);
  for (size_t i = 0; i < len; i++) {
    val->of.list.data[i].kind = WASMTIME_COMPONENT_BOOL;
  }
  val->kind = WASMTIME_COMPONENT_LIST;
}

static inline size_t go_component_val_record_len(const wasmtime_component_val_t *val) {
  return val->of.record.size;
}

static inline wasmtime_component_val_t *go_component_val_record_at(wasmtime_component_val_t *val, size_t i) {
  return &val->of.record.data[i].val;
}

static inline size_t go_component_val_record_name_len(const wasmtime_component_val_t *val, size_t i) {
  return val->of.record.data[i].name.size;
}

static inline const char *go_component_val_record_name_data(const wasmtime_component_val_t *val, size_t i) {
  const wasm_name_t *name = &val->of.record.data[i].name;
  return name->size ? (const char *)name->data : NULL;
}

// go_component_val_record_init sets `val` to a record of `len` fields with
// empty names and bool values, which own nothing.
static inline void go_component_val_record_init(wasmtime_component_val_t *val, size_t len) {
  wasmtime_component_valrecord_new_uninit(&val->of.record, len);
  for (size_t i = 0; i < len; i++) {
    wasm_byte_vec_new_empty(&val->of.record.data[i].name);
    val->of.record.data[i].val.kind = WASMTIME_COMPONENT_BOOL;
  }
  val->kind = WASMTIME_COMPONENT_RECORD;
}

// go_component_val_record_set_name sets the name of field `i` of `val` to a
// copy of the `len` bytes at `data`.
static inline void go_component_val_record_set_name(wasmtime_component_val_t *val, size_t i, const char *data, size_t len) {
  wasm_byte_vec_delete(&val->of.record.data[i].name);
  wasm_byte_vec_new(&val->of.record.data[i].name, len, (const wasm_byte_t *)data);
}

// go_component_val_vec_delete frees the list or record in `val` and sets it to
// a bool, which owns nothing.
static inline void go_component_val_vec_delete(wasmtime_component_val_t *val) {
  if (val->kind == WASMTIME_COMPONENT_LIST) {
    wasmtime_component_vallist_delete(&val->of.list);
  } else if (val->kind == WASMTIME_COMPONENT_RECORD) {
    wasmtime_component_valrecord_delete(&val->of.record);
  }
  val->kind = WASMTIME_COMPONENT_BOOL;
}

#endif
