# Ubuntu VM images that boot on a GPU preset.
data "hivenet_images" "ubuntu_gpu" {
  type          = "vm"
  name_regex    = "^Ubuntu"
  gpu_supported = true
}

# Container templates you saved in the console.
data "hivenet_images" "my_templates" {
  type = "container"
  mine = true
}

output "ubuntu_gpu_image_ids" {
  value = data.hivenet_images.ubuntu_gpu.images[*].id
}
