# trafficctl Kullanim Kilavuzu

## 1. Projenin Amaci

`trafficctl`, Kubernetes uzerinde Gateway API `HTTPRoute` backend agirliklarini
politika tabanli sekilde yoneten bir controller projesidir.

Temel fikir sunudur:

- Uygulamanin birden fazla surumu ayni route arkasinda calisir.
- Bu surumlerin trafik paylari manuel degil, deklaratif bir policy ile tanimlanir.
- Controller Prometheus benzeri bir kaynaktan metrik okur.
- Bir backend sagliksiz gorunurse trafik ondan daha saglikli backend'lere kaydirilir.
- Kaydirma islemi kontrolsuz degil, belirlenen sinirlar icinde ve adim adim yapilir.

Portfoy acisindan bu proje sunlari gosterir:

- Kubebuilder ile CRD ve controller gelistirme
- Kubernetes reconciliation mantigini kavrama
- Gateway API ile calisma
- Prometheus veri modeliyle entegrasyon
- Karar motorunu controller'dan ayiran temiz bir mimari kurma
- Test, gozlemlenebilirlik ve operator ergonomisi dusunme

## 2. Proje Ne Problemi Cozuyor

Ornek senaryo:

- `auth-v1` karali surum
- `auth-v2` yeni veya canary surum
- Ikisi de ayni `HTTPRoute` arkasinda trafik aliyor

Bu durumda su ihtiyac dogar:

- Yeni surume kontrollu trafik verilsin
- Gecikme veya hata orani artarsa trafik geri cekilsin
- Hicbir backend tamamen acliga itilmesin
- Trafik degisimleri bir anda degil, kontrollu sekilde olsun

`trafficctl` tam olarak bu akis icin yazilmis bir operator iskeleti sunar.

## 3. Genel Calisma Mantigi

Projenin cekirdek mantigi su dongu etrafinda kurulu:

1. Kullanici bir `TrafficPolicy` olusturur.
2. Controller bu policy'yi reconcile eder.
3. Ilgili `HTTPRoute` ve backend tanimlarini okur.
4. Prometheus sorgularindan backend bazli metrikleri toplar.
5. Bu metrikleri esiklerle karsilastirir.
6. Gerekirse yeni agirlik dagilimini hesaplar.
7. Sonucu `HTTPRoute` uzerindeki backend weight alanlarina yazar.
8. Status, condition, event ve Prometheus metric'lerini gunceller.

Bu mantik kabaca su sekilde ozetlenebilir:

```text
TrafficPolicy -> Controller -> Metric oku -> Karar ver -> HTTPRoute guncelle
```

Kod seviyesinde akisin parcalari sunlardir:

- [api/v1alpha1/trafficpolicy_types.go](../api/v1alpha1/trafficpolicy_types.go)
  `TrafficPolicy` CRD tanimini icerir.
- [internal/controller/trafficpolicy_controller.go](../internal/controller/trafficpolicy_controller.go)
  reconcile akisinin merkezidir.
- [internal/metrics/prometheus.go](../internal/metrics/prometheus.go)
  Prometheus sorgusu calistirip backend bazli veri toplar.
- [internal/evaluator/evaluator.go](../internal/evaluator/evaluator.go)
  hangi backend'in trafik kaybetmesi veya kazanmasi gerektigine karar verir.
- [internal/router/httproute.go](../internal/router/httproute.go)
  hesaplanan agirliklari `HTTPRoute` objesine uygular.
- [internal/cli/cli.go](../internal/cli/cli.go)
  `trafficctl` CLI komutlarini saglar.

## 4. TrafficPolicy Nesnesi Ne Soyler

Bir `TrafficPolicy`, controller'a su talimati verir:

- Hangi route'u yoneteceksin
- Hangi backend'ler trafik aliyor
- Bu backend'lerin minimum ve maksimum agirliklari ne olacak
- Hangi metrikler izlenecek
- Esik degerler neler olacak
- Tek seferde en fazla ne kadar agirlik kaydirabileceksin
- Degisimler arasinda ne kadar bekleyeceksin
- Politika gecici olarak durdurulacak mi

Ornek kaynak:

```yaml
apiVersion: traffic.traffic.devops.io/v1alpha1
kind: TrafficPolicy
metadata:
  name: auth-api
spec:
  routeName: auth-route
  backends:
    - name: auth-v1
      minWeight: 20
      maxWeight: 100
    - name: auth-v2
      minWeight: 10
      maxWeight: 80
  metrics:
    latency:
      query: histogram_quantile(0.95, rate(http_request_duration_seconds_bucket{service="auth"}[1m]))
      thresholdMs: 500
    errorRate:
      query: 100 * sum by (backend) (rate(http_requests_total{service="auth",status=~"5.."}[1m])) / sum by (backend) (rate(http_requests_total{service="auth"}[1m]))
      thresholdPercent: 2
  strategy:
    cooldownSeconds: 30
    maxStepPercent: 20
    movingAverageWindow: 3
```

## 5. Karar Verme Kurallari

Controller'in davranisi sade ama mantikli kurallara dayanir:

- Bir backend, tanimli sinyallerden herhangi biri esigi asarsa sagliksiz sayilir.
- Trafik tek hamlede degil, `maxStepPercent` kadar kaydirilir.
- Backend agirliklari `minWeight` ve `maxWeight` sinirlarini asamaz.
- Kaydirmalar arasinda `cooldownSeconds` kadar beklenir.
- Gecici sivrilmeleri yumusatmak icin `movingAverageWindow` kullanilir.
- Metrik kaynagi bozulursa sistem fail-safe davranir ve agirliklari dondurur.
- `.spec.paused=true` ise policy izlenmeye devam eder ama agirlik degisikligi uygulanmaz.

Bu sayede sistem agresif degil, kontrollu hareket eder.

## 6. Status ve Fazlar Ne Anlama Gelir

Controller, `TrafficPolicy.status` alanini doldurarak mevcut durumu aciklar.

Baslica fazlar:

- `Pending`: Henuz ilk degerlendirme tamamlanmadi
- `Stable`: Her sey sinirlar icinde, degisim gerekmiyor
- `Progressing`: Trafik aktif olarak kaydiriliyor
- `Frozen`: Politika duraklatildi veya metrik kaynagi sorunlu
- `Degraded`: Route eksik, spec gecersiz veya operator ilgisi gerekiyor

Bu tasarim portfoyde guzel gorunur cunku sadece spec degil, operator deneyimi de dusunulmustur.

## 7. Kurulum Icin Gereksinimler

Yerel gelistirme ve demo icin su araclar faydalidir:

- Go
- Docker
- kubectl
- Kind
- make

Projede kullanilan hedefler `Makefile` icinde tanimlidir:

- [Makefile](../Makefile)

## 8. Projeyi Yerelde Calistirma

### Secenek A: Test ve derleme ile baslamak

Depo kokunde sunlari calistir:

```sh
make build
make build-cli
make test
```

Bu komutlar sunlari yapar:

- controller binary'sini uretir
- CLI binary'sini uretir
- manifest ve generated kodlari yeniler
- format ve vet kontrollerini kosar
- unit ve envtest tabanli testleri calistirir

### Secenek B: Controller'i dogrudan yerelde calistirmak

Kubernetes context'in hazirsa:

```sh
make run
```

Not:

- Bu komut mevcut kubeconfig context'ini kullanir.
- Prometheus adresi verilmezse controller metrik bazli kaydirma yapmaz, mevcut agirliklari korur.

Prometheus ile calistirmak icin:

```sh
go run ./cmd/main.go --prometheus-address=http://prometheus.monitoring:9090
```

## 9. Demo veya Ornek Akis

Repoda ornek policy dosyasi hazir geliyor:

- [config/samples/traffic_v1alpha1_trafficpolicy.yaml](../config/samples/traffic_v1alpha1_trafficpolicy.yaml)

Bu dosyayi uygulamak icin:

```sh
kubectl apply -k config/samples/
```

Policy durumunu gormek icin:

```sh
kubectl get trafficpolicies
kubectl describe trafficpolicy auth-api
```

Veya CLI ile:

```sh
make build-cli
./bin/trafficctl list
./bin/trafficctl status auth-api
```

## 10. Gercek Cluster'a Kurulum Adimlari

Asagidaki akista kendi container image kaydini kullanman gerekir.

### 1. Image olustur ve registry'ye push et

```sh
export IMG=<registry>/<kullanici>/trafficctl:tag
make docker-build docker-push IMG=$IMG
```

### 2. CRD'leri kur

```sh
make install
```

### 3. Controller'i deploy et

```sh
make deploy IMG=$IMG
```

Eger metrik tabanli kararlar aktif olsun istiyorsan manager'i uygun flag ile calistirman gerekir.
Bu repo icinde deploy manifestleri genel scaffold yapisinda oldugu icin, gerekiyorsa manager argumanlarini
`config/manager/manager.yaml` uzerinden ozellestirebilirsin.

Prometheus ile beklenen mantik su:

- Controller, Prometheus HTTP API'ye erisir
- Sorgu sonucunda backend bazli bir vector doner
- Backend etiketi varsayilan olarak `backend` kabul edilir

Ilgili flag'ler:

- `--prometheus-address`
- `--prometheus-backend-label`
- `--prometheus-timeout`

## 11. CLI Kullanimi

Projeye dahil `trafficctl` CLI araci operator kullanimini kolaylastirir.

Derlemek icin:

```sh
make build-cli
```

Komutlar:

```sh
./bin/trafficctl list
./bin/trafficctl list -A
./bin/trafficctl status auth-api
./bin/trafficctl freeze auth-api
./bin/trafficctl resume auth-api
```

Ne ise yarar:

- `list`: policy'leri listeler
- `status NAME`: tek policy'nin detay durumunu verir
- `freeze NAME`: `.spec.paused=true` yapar
- `resume NAME`: `.spec.paused=false` yapar

## 12. Metrik Sorgulari Hakkinda

Bu projede metrik sorgulari backend bazli sonuc donmelidir.
Yani sorgu sonucu "hangi backend ne kadar latency veya error rate uretti"
bilgisini tasimalidir.

Prometheus entegrasyonunun kritik noktasi sudur:

- Sonuc `vector` tipinde olmali
- Her sample icinde backend'i ayirt eden bir label olmali
- Varsayilan label adi `backend`'dir

Ornek hata orani sorgusu:

```promql
100 * sum by (backend) (rate(http_requests_total{service="auth",status=~"5.."}[1m]))
/
sum by (backend) (rate(http_requests_total{service="auth"}[1m]))
```

## 13. Gozlemlenebilirlik

Controller kendi davranisini sadece status ile degil, event ve metric ile de aciklar.

Izlenebilecek seyler:

- weight degisimi oldu mu
- freeze durumu neden oldu
- metrik kaynagi hata veriyor mu
- su an backend agirliklari ne
- policy hangi fazda

Kod:

- [internal/controller/observability.go](../internal/controller/observability.go)

Bu da projeyi portfoyde daha guclu gosterir cunku sadece controller yazmadigini,
operasyonel gorunurlugu de dusundugunu gosterir.

## 14. Projenin Ic Yapisi

Depodaki temel klasorler:

- `api/v1alpha1`: CRD tipi ve schema tanimlari
- `cmd`: manager ve CLI giris noktalari
- `internal/controller`: reconcile mantigi
- `internal/evaluator`: trafik karar motoru
- `internal/metrics`: Prometheus ve metric source soyutlamasi
- `internal/router`: `HTTPRoute` uzerine weight yazma mantigi
- `config`: CRD, RBAC, manager deployment ve sample manifestler
- `test/e2e`: e2e testleri

Bu yapi, projeyi anlatirken mimari ayrimi gostermek icin guzel bir referans olur.

## 15. Gelistirme ve Test Akisi

Kod degisikligi yaptiktan sonra tipik akis:

```sh
make manifests
make generate
make lint-fix
make test
```

E2E testleri icin:

```sh
make test-e2e
```

Not:

- E2E testler izole bir Kind cluster bekler.
- Bu, gercek cluster'ini bozmayacak sekilde dogru bir test yaklasimidir.

## 16. Projeyi Sunarken Nasil Anlatabilirsin

Bu projeyi portfoyunde veya mulakatta su sekilde anlatabilirsin:

`trafficctl`, Gateway API uzerindeki backend trafik dagilimini Prometheus metriklerine gore yoneten bir Kubernetes operator prototipidir. Kullanicinin tanimladigi policy'yi reconcile eder, backend bazli sinyal toplar, min-max sinirlari ve cooldown kurallariyla yeni agirlik hesaplar, sonra bunu HTTPRoute uzerine uygular. Ayrica status conditions, events, Prometheus metrics ve operator CLI ile gozlemlenebilir bir deneyim sunar.

Bu anlatim projeyi "sadece bir controller" olmaktan cikarip "iyi dusunulmus bir platform bileşeni" gibi gosterir.

## 17. Ozet

Bu projenin ana fikri sunudur:

- Trafik yonetimini deklaratif hale getirmek
- Metrik tabanli karar alabilen bir Kubernetes operator yazmak
- Bu mantigi temiz katmanlara ayirmak
- Kullanici ve operator deneyimini status, CLI ve observability ile desteklemek

Portfoy icin bakildiginda proje gayet iyi bir mesaj veriyor:

- Kubernetes operator yazabiliyorsun
- API tasarlayabiliyorsun
- controller mantigini anlayarak kodlayabiliyorsun
- sadece kod degil sistem davranisi da dusunuyorsun
