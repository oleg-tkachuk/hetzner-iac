// Package values hands a chart's rendered values to a Helm release.
//
// Two functions, and what is left here is exactly the half that needs Pulumi's
// SDK: the templates, the data they are executed against and the rendering all
// live in internal/pkg/charts now, beside the chart that names them. That
// package must not reach the SDK — every gate and every command-line tool
// imports it — so the asset wrappers stay on this side of the line.
//
// A template cannot see a Pulumi output, because an output has no value when
// the program builds its inputs. Asset takes the resolved data instead: the
// layer applies the outputs it needs and hands the result over, so the
// dependency still reaches the engine.
package values

import (
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/charts"
)

// Static renders a chart whose values need nothing from the cluster, ready to
// hand to a Helm release.
func Static(chart string, data any) (pulumi.AssetOrArchiveArrayInput, error) {
	rendered, err := charts.Render(chart, data)
	if err != nil {
		return nil, err
	}

	return pulumi.AssetOrArchiveArray{pulumi.NewStringAsset(rendered)}, nil
}

// Asset renders a chart whose values need something the cluster tier
// published — a name, a CIDR, a count.
//
// data resolves to the value the template is executed against. Threading the
// outputs through it rather than reading them inside the template is what
// keeps the dependency visible to the engine: the release waits for the same
// outputs it would have waited for as map inputs.
func Asset(chart string, data pulumi.Output) pulumi.AssetOrArchiveArrayInput {
	return pulumi.AssetOrArchiveArray{
		// Unchecked, and the last one in this package. ApplyT's result type
		// follows from the callback's signature three lines below, so the
		// assertion cannot be wrong while that signature is in view. The typed
		// pulumix form does not fit: it takes pulumix.Input[T], and `data` is
		// the pulumi.Output interface that layer.Component.ValuesFrom returns.
		data.ApplyT(func(resolved any) (pulumi.AssetOrArchive, error) {
			rendered, err := charts.Render(chart, resolved)
			if err != nil {
				return nil, err
			}

			return pulumi.NewStringAsset(rendered), nil
		}).(pulumi.AssetOrArchiveOutput),
	}
}
