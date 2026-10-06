curl --request POST \
 --url https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary \
 --header 'authorization: Bearer $API_KEY' \
 --header 'user-agent: antigravity/cli/1.3.0 (aidev_client; os_type=linux; arch=amd64; cl=994000447; auth_method=consumer)' \
 --data '
{
  "project": "$project-id"
}
'

jq '.models |= (to_entries | sort_by(.key) | from_entries)' antigravity/agy-models-new.json > antigravity/agy-models.json

git reset --soft HEAD~1