# Running 

```sh
PROJECT_ID=my-project
gcloud iam service-accounts create configconnector-test
gcloud projects add-iam-policy-binding $PROJECT_ID \
    --member="serviceAccount:configconnector-test@$PROJECT_ID.iam.gserviceaccount.com" \
    --role="roles/owner"
gcloud iam service-accounts add-iam-policy-binding \
configconnector-test@PROJECT_ID.iam.gserviceaccount.com \
    --member="serviceAccount:$PROJECT_ID.svc.id.goog[cnrm-system/cnrm-controller-manager]" \
    --role="roles/iam.workloadIdentityUser"
```
