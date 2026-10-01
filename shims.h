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
    const wasmtime_component_resource_type_t *ty
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
  UNION_ACCESSOR(wasmtime_component_val, string, wasm_name_t) \
  UNION_ACCESSOR(wasmtime_component_val, list, wasmtime_component_vallist_t) \
  UNION_ACCESSOR(wasmtime_component_val, record, wasmtime_component_valrecord_t) \
  UNION_ACCESSOR(wasmtime_component_val, resource, wasmtime_component_resource_any_t*)

#define UNION_ACCESSOR(name, field, ty) \
  ty go_##name##_##field##_get(const name##_t *val); \
  void go_##name##_##field##_set(name##_t *val, ty i);

EACH_UNION_ACCESSOR(UNION_ACCESSOR)

#undef UNION_ACCESSOR
