

# new method auth google

## agy-cli will generate this
https://accounts.google.com/o/oauth2/auth?access_type=offline&client_id=$CLIENT_ID&code_challenge=$CODE_CHALLENGE&code_challenge_method=S256&prompt=consent&redirect_uri=https%3A%2F%2Fantigravity.google%2Foauth-callback&response_type=code&scope=https%3A%2F%2Fwww.googleapis.com%2Fauth%2Fcloud-platform+https%3A%2F%2Fwww.googleapis.com%2Fauth%2Fuserinfo.email+https%3A%2F%2Fwww.googleapis.com%2Fauth%2Fuserinfo.profile+https%3A%2F%2Fwww.googleapis.com%2Fauth%2Fcclog+https%3A%2F%2Fwww.googleapis.com%2Fauth%2Fexperimentsandconfigs+https%3A%2F%2Fwww.googleapis.com%2Fauth%2Faicode+openid&state=2ht8mPAlRq9ezOQA_ImZzQ

### generate code_verifier
code_verifier=$(openssl rand -base64 32 | tr -d '=' | tr '/+' '_-')
echo "Verifier Anda: $code_verifier"

### generate code_challenge
code_challenge=$(printf %s "$code_verifier" | openssl dgst -sha256 -binary | base64 | tr -d '=' | tr '/+' '_-')
echo "Challenge Anda: $code_challenge"

### Cara Kerjanya (Kenapa butuh keduanya?)
1. Anda membuka browser dengan URL login yang memuat code_challenge. Server Google mencatat, "Oke, orang yang sedang login ini punya challenge = XYZ".
2. Anda login dan mendapat authorization code.
3. Anda menukar kode tersebut via curl sambil mengirimkan code_verifier (asli/rahasianya).
4. Server Google akan menghitung ulang code_verifier yang baru saja Anda kirim. Jika hasil hitungannya cocok dengan code_challenge yang disimpan di langkah 1, server akan memberikan token. Jika tidak cocok, request ditolak. (Ini mencegah hacker yang berhasil mencuri authorization code Anda untuk mendapatkan token, karena hacker tersebut tidak tahu code_verifier aslinya).

 
## get a access_token (temp), refresh_token, id_token. Need code to exchange (code from user/google web)
curl --request POST \
 --url https://oauth2.googleapis.com/token \
 --header 'content-type: application/x-www-form-urlencoded' \
 --header 'user-agent: Go-http-client/1.1' \
 --json 'client_id=$CLIENT_ID&client_secret=$CLIENT_SECRET&code=$CODE&code_verifier=$CODE_VERIFIER&grant_type=authorization_code&redirect_uri=https%3A%2F%2Fantigravity.google%2Foauth-callback' > token.json

## get new access_token using refresh_token
curl --request POST \
 --url https://oauth2.googleapis.com/token \
 --compressed \
 --header 'accept-encoding: gzip' \
 --header 'content-type: application/x-www-form-urlencoded' \
 --header 'host: oauth2.googleapis.com' \
 --header 'user-agent: Go-http-client/1.1' \
 --data 'client_id=$CLIENT_ID&client_secret=$CLIENT_SECRET&grant_type=refresh_token&refresh_token=$REFRESH_TOKEN' > refresh-token.json

## get identity (email)
curl --request GET \
 --url https://www.googleapis.com/oauth2/v2/userinfo \
 --header 'authorization: Bearer $ACCESS_TOKEN' \
 --header 'user-agent: Go-http-client/1.1' > userinfo.json


## get project-id
curl --request POST \
 --url https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist \
 --header 'authorization: Bearer $ACCESS_TOKEN' \
 --header 'user-agent: antigravity/cli/1.3.0 (aidev_client; os_type=linux; arch=amd64; cl=994000447; auth_method=consumer)' \
 --json '{"metadata": {"ideType": "ANTIGRAVITY"}}' > load-code-assist.json


# user quota
curl --request POST \
 --url https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary \
 --header 'authorization: Bearer $ACCESS_TOKEN' \
 --header 'user-agent: antigravity/cli/1.3.0 (aidev_client; os_type=linux; arch=amd64; cl=994000447; auth_method=consumer)' \
 --json '{"project": "$project-id"}' > user-quota-summary.json


# get model list
curl --request POST \
 --url https://daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels \
 --header 'authorization: Bearer $ACCESS_TOKEN' \
 --header 'user-agent: antigravity/cli/1.3.0 (aidev_client; os_type=linux; arch=amd64; cl=994000447; auth_method=consumer)' \
 --json '{"project": "$project-id"}' > agy-models.json

 
# get config (looks like contains a lot metadata regarding models)
curl --request GET \
 --url https://aicode.googleapis.com/v1:fetchConfig \
 --header 'authorization: Bearer $ACCESS_TOKEN' \
 --header 'user-agent: antigravity/cli/1.3.0 (aidev_client; os_type=linux; arch=amd64; cl=994000447; auth_method=consumer)' > fetch-config.json

# title generator
curl --request POST \
 --url 'https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse' \
 --header 'authorization: Bearer $ACCESS_TOKEN' \
 --header 'user-agent: antigravity/cli/1.3.0 (aidev_client; os_type=linux; arch=amd64; cl=994000447; auth_method=consumer)' \
 --json @title-generator.json


# chat completions
curl --request POST \
 --url 'https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse' \
 --header 'authorization: Bearer $ACCESS_TOKEN' \
 --header 'user-agent: antigravity/cli/1.3.0 (aidev_client; os_type=linux; arch=amd64; cl=994000447; auth_method=consumer)' \
 --json @req-stream-gemini-3-8-flash-high.json > resp-stream-gemini-3-8-flash-high.json



 