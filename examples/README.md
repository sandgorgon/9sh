# Examples

Eight runnable `.ky` scripts, one feature area each, verified against
the real binary (not just written prose). Run any of them directly:

```
9sh examples/01_namespace_and_files.ky
```

| File | Covers |
|---|---|
| `01_namespace_and_files.ky` | `bind`, `glob`, `ls`, `stat`, `cat`, `format_time`/`humanize_time` |
| `02_jobs_and_processes.ky` | `%cmd`, backgrounding with `&`, `j.status`/`j \| wait`, `ps()`, `&&`/`\|\|` chaining, `exit_code()` |
| `03_data_pipelines.ky` | `where`/`select`/`sort_by`/`group_by`/`each`, `sum`/`min`/`max`/`avg`/`any`, `round` |
| `04_strings_and_formatting.ky` | `split`/`trim`/`join`/`contains`/`starts_with`/`ends_with`/`index_of`, `pad_left`/`pad_right`/`repeat`/`len`, `to_int`, `format` |
| `05_control_flow_and_closures.ky` | `while`/`break`/`continue`, self-recursive closures, default parameters, `error()`/`?`, errors-as-values in a pipeline |
| `06_env_and_kyu_vars.ky` | `getenv`/`setenv`/`unsetenv` (real `/env` files), `vars()`/`unset()` (plain kyu variables) |
| `07_remote_and_local_namespaces.ky` | `dir()`, `join_path`/`path`, `dial()`'s graceful-failure behavior — plus a commented sketch of `dial`+`bind`+`@host{}` against a real peer |
| `08_script_args_and_file_ops.ky` | a script's own `args`, `cp`/`mv`/`rm`/`find` | 

## Important: script mode only prints the *last* expression

`9sh script.ky` runs the whole file as one program and prints only the
value of its final statement — every `bind`/`:=` before that still
happens, it's just silent (see README.md's "Startup sequence" and
`kyu/eval/eval.go`'s `Eval`: "runs a full program and returns the
value of its last statement"). That's why each script here ends with
one `format(...)` call tying together everything it just set up,
rather than printing as it goes.

The interactive TUI and the plain `-repl` behave differently: each
submitted block prints its own result immediately, the way a real
REPL does. To see every intermediate step instead of just the final
summary line, open `9sh` (or `9sh -repl`) and paste each script in a
few statements at a time.

## Notes

- These are meant to be read, not just run — every non-obvious builtin
  has a one-line comment explaining *why* it works the way it does,
  matching README's own style.
- `07` and `08` bind real host locations (`dir("/tmp")`, `/local`) so
  they're fully self-contained and safe to run repeatedly — `08`
  copies `LICENSE` into a scratch path under `/tmp`, renames it, then
  removes it again, never touching anything in this repo.
- None of these need `9vcs`, a remote peer, or any config beyond a
  fresh install's defaults.
