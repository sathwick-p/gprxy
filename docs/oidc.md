# OIDC Implementation

Oauth2.0 : 

OAuth 2.0 is an authorisation framework that enables users to safely share their data between different applications. It is an industry standard that addresses the API security concerns associated with sharing user crds.

Problems oauth solves : 
1. User credential exposure : Before oauth the user would need to share their user name and password for tha application which would increase the security risk.
2. Scope of access ; Before Oauth, the app might have access to data that the user did not actually wish to want to share. 
3. No way to revoke access : Before Oauth the user could not easily restrict or revoke the app's access to their data. While user could decide to change their app password it would affect all of the third party applications they had previously authorised

Step by step breakdown :
1. The client asks the user for access to their resources on the server
2. the user grants access to the client through the authorisation server by loggin in with their creds and are not shared with the client. instead an auth code is generated and shared with the client 
3. The client uses this auth code to request an access token from an endpoint that is provided by the auth server
4. The auth server generates and returns an access token which the client can use to access the user's resource on the server

![alt text](https://blog.postman.com/wp-content/uploads/2023/08/OAuth-2.0.png)