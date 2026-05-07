for ns in basic-resourceclaimtemplate basic-multiple-requests basic-shared-claim-across-containers basic-shared-claim-across-pods basic-resourceclaim-opaque-config; do \
  echo "${ns}:"
  for pod in $(kubectl get pod -n ${ns} --output=jsonpath='{.items[*].metadata.name}'); do \
    for ctr in $(kubectl get pod -n ${ns} ${pod} -o jsonpath='{.spec.containers[*].name}'); do \
      echo "${pod} ${ctr}:"
      kubectl logs -n ${ns} ${pod} -c ${ctr}| grep -E "GPU_DEVICE_[0-9]+" | grep -v "RESOURCE_CLAIM"
    done
  done
  echo ""
done
