data "hivenet_regions" "all" {}

output "regions" {
  value = data.hivenet_regions.all.names
}
