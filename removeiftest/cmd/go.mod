module github.com/dashpole/dashpole_demos/removeiftest/cmd

go 1.17

require go.opentelemetry.io/collector/model v0.44.0

require github.com/gogo/protobuf v1.3.2 // indirect

replace go.opentelemetry.io/collector/model v0.44.0 => github.com/dashpole/opentelemetry-collector/model v0.0.0-20220228142245-5de260f3bf9f
