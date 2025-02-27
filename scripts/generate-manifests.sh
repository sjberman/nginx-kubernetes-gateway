#!/usr/bin/env bash

# Generate deployment files using Helm. This script uses the Helm chart examples in examples/helm

charts=$(find examples/helm -maxdepth 1 -mindepth 1 -type d -exec basename {} \;)

generate_manifests() {
    chart=$1
    manifest=deploy/${chart}/deploy.yaml
    mkdir -p deploy/${chart}

    helm_parameters="--namespace nginx-gateway --set nameOverride=nginx-gateway --skip-crds"
    if [ "${chart}" == "openshift" ]; then
        chart="default"
        helm_parameters="${helm_parameters} --api-versions security.openshift.io/v1/SecurityContextConstraints"
    fi

    helm template nginx-gateway ${helm_parameters} --values examples/helm/${chart}/values.yaml charts/nginx-gateway-fabric >${manifest} 2>/dev/null
    sed -i.bak '/app.kubernetes.io\/managed-by: Helm/d' ${manifest}
    sed -i.bak '/helm.sh/d' ${manifest}
    cp ${manifest} config/base
    kubectl kustomize config/base >${manifest}
    rm -f config/base/deploy.yaml
    rm -f ${manifest}.bak
}

for chart in ${charts}; do
    generate_manifests ${chart}
done

# For OpenShift, we don't need a Helm example so we generate the manifests from the default values.yaml
generate_manifests openshift
