## 0.1.0 (Unreleased)

FEATURES:

* **Provider configuration:** `token` and `endpoint`, or the `HIVENET_TOKEN` and `HIVENET_ENDPOINT` environment variables
* **New Data Source:** `hivenet_regions`
* **New Data Source:** `hivenet_presets`
* **New Data Source:** `hivenet_images`
* **New Data Source:** `hivenet_instance`
* **New Resource:** `hivenet_instance`: create from a preset and image, or import an existing instance; set `power_state`, destroy. Every other change replaces the instance
