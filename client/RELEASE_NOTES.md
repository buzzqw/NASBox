# NASBox 1.28.9

Corretto il loop di "PULL_DEFERRED": le cancellazioni locali già applicate sul
NAS restavano nella coda `pending` e facevano rinviare il pull all'infinito.
Ora vengono riconosciute e rimosse automaticamente.
