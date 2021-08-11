
⚠️ **This project is deprecated. Please see [cortextool analyse](https://github.com/grafana/cortex-tools#analyse) which provides the same features** ⚠️

# mixin-metrics
![License](https://img.shields.io/github/license/hjet/mixin-metrics?color=blue)

Extract prometheus metrics from dashboard JSON and rules YAML.

## Prerequisites
- Go 1.16

## Compile
```
go build mixin-metrics.go
```

## Use
Parse rules configs in `DIR`
```
mixin-metrics --dir=DIR --out="metrics_out.json" rules
```
Replace `DIR` with directory containing Prometheus rules YAML files. By default will save parsed metrics in `metrics_out.json`.

Parse dashboard JSON files in `DIR`
```
mixin-metrics --dir=DIR dash 
```
Similar to above. Parses Grafana dashboard JSON files.

Join and print parsed metrics in Prom relabel-config format
```
mixin-metrics --dir=DIR --print dash
```
Use this output with `relabel_config` to `drop` or `keep` needed metrics. See [Reducing Prometheus metrics usage with relabeling](https://grafana.com/docs/grafana-cloud/billing-and-usage/prometheus/usage-reduction/#reducing-prometheus-metrics-usage-with-relabeling) to learn more.

## TODO
- [ ] fail fast if parsing rules/dash with wrong flag
- [ ] better docs
- [ ] dashboard structs (no jq)
- [ ] tests
- [ ] binaries
