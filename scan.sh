for ns in basic-resourceclaimtemplate media; do \
  echo "${ns}:"
  for pod in $(kubectl get pod -n ${ns} --output=jsonpath='{.items[*].metadata.name}'); do \
    for ctr in $(kubectl get pod -n ${ns} ${pod} -o jsonpath='{.spec.containers[*].name}'); do \
      echo "${pod} ${ctr}:"
      kubectl logs -n ${ns} ${pod} -c ${ctr}| grep -E "MEM_DEVICE*"
    done
  done
  echo ""
done
