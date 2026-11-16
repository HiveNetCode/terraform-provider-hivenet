data "hivenet_presets" "rtx4090_fr" {
  region           = "FR"
  free_text_search = "RTX 4090"
}

resource "hivenet_instance" "web" {
  name       = "web-1"
  preset_id  = data.hivenet_presets.rtx4090_fr.presets[0].id
  image_type = "vm"
  image_id   = "ubuntu24.04"

  lifecycle {
    # Any change except power_state replaces the instance and deletes its disk.
    prevent_destroy = true
  }
}
