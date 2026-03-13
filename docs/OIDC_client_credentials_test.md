## Create your client with client_credentials grant type

Skip this step if you already have a client.

```bash
curl http://localhost:4000/api/auth/oauth2/create-client \
 --request POST \
 --header 'Content-Type: application/json' \
 --header 'Accept: application/json' \
 --header 'Authorization: Bearer X1wA6c0S1G8iZlgIVKGzYbgtJHUxzUeBvpLEwFhw4cg=' \
 --cookie 'apiKeyCookie=YOUR_SECRET_TOKEN' \
 --data '{
"redirect_uris": [
"http://localhost:8080/callback"
],
"scope": "openid",
"client_name": "test_client",
"client_uri": "http://localhost:8080",
"logo_uri": "",
"contacts": [
"jesus_mata"
],
"token_endpoint_auth_method": "client_secret_basic",
"grant_types": [
"client_credentials"
],
"response_types": [
"code"
],
"type": "web"
}'
```

## Get token from OIDC

```bash
curl -X POST http://localhost:4000/api/auth/oauth2/token \
    -H "Content-Type: application/x-www-form-urlencoded" \
    -d "grant_type=client_credentials&client_id=qYbytbruEyPiEkZgSLJAoxijRLzujPPJ&client_secret=gxdGateIrcoszPiTtmIJPjDTGBiyKjNU&resource=api"
```

## Use the token to access protected resources

```bash
curl -X GET http://localhost:4000/api/ \
    -H "Authorization: Bearer <token>"
```

Eg.

```bash
curl http://localhost:8080/factura/api/Test \
    -H "Authorization: Bearer eyJhbGciOiJFZERTQSIsImtpZCI6IkRESEw5Sno3ODNETFJwOHQ2bHAzeHd3dXV5d2MxcGZvIn0.eyJhdWQiOlsiYXBpIiwiaHR0cDovL2xvY2FsaG9zdDo0MDAwL2FwaS9hdXRoL29hdXRoMi91c2VyaW5mbyJdLCJhenAiOiJxWWJ5dGJydUV5UGlFa1pnU0xKQW94aWpSTHp1alBQSiIsInNjb3BlIjoib3BlbmlkIiwiaXNzIjoiaHR0cDovL2xvY2FsaG9zdDo0MDAwIiwiaWF0IjoxNzczMzcwOTcwLCJleHAiOjE3NzMzNzQ1NzB9.v-zb7-UOJZyQxFc6yWwDZ6aFVZMPxtpqDUQQQtYZJYnUcJjzQwWPSbZbWNv5l9XdtpiTLdYvMaoioxPXvIHbDw"
```
