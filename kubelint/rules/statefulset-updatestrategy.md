# statefulset-updatestrategy

StatefulSet updateStrategy should be explicitly set.

## Description

OnDelete is the default updateStrategy for StatefulSets, which requires manual pod deletion to trigger updates. 
This is often not what users expect. We require updateStrategy to be explicitly set to signal intentionality, 
preferably to `RollingUpdate`.

## How to fix

Set `spec.updateStrategy.type` explicitly in your StatefulSet manifest:

```yaml
kind: StatefulSet
spec:
  updateStrategy:
    type: RollingUpdate
```
