ARG BASE_IMAGE=public.ecr.aws/eks-distro-build-tooling/eks-distro-minimal-base:latest
FROM ${BASE_IMAGE}

ARG TARGETOS=linux
ARG TARGETARCH=amd64

COPY _output/LICENSES /LICENSES
COPY _output/ATTRIBUTION.txt /ATTRIBUTION.txt
COPY bin/${TARGETOS}/${TARGETARCH}/gateway /gateway

ENTRYPOINT ["/gateway"]
