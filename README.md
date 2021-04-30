# mixin-metrics

Extract prometheus metrics from dashboard JSON and rules YAML

## Usage:

Parse rules configs in `DIR`
```
mixin-metrics --dir=DIR rules
```

Parse dashboard JSON files in `DIR`
```
mixin-metrics --dir=DIR dash 
```

Join and print parsed metrics in Prom relabel-config format
```
mixin-metrics --dir=DIR --print dash
```

## TODO

- [] fail fast if parsing rules/dash with wrong flag
- [] license, move to graf repo, etc.
- [] longer tutorial and incoporate in metrics reduction docs
