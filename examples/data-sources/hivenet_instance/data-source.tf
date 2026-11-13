data "hivenet_instance" "training" {
  id = "3fa85f64-5717-4562-b3fc-2c963f66afa6"
}

output "training_status" {
  value = data.hivenet_instance.training.status
}
