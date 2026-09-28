# Trafficflow

Intern nätverkskarta för MikroTik RouterOS 7 och SwOS. Appen läser portstatus och trafikräknare via SNMP, visar trafik på länkar och sparar historik i 30 dagar. RouterOS-enheter kan föreslå länkar via LLDP-MIB. Övriga länkar skapas manuellt.

## Starta

1. Kopiera `.env.example` till `.env`.
2. Sätt `ADMIN_PASSWORD` till ett unikt lösenord med minst 10 tecken.
3. Skapa `APP_KEY` med `openssl rand -base64 32` och lägg värdet i `.env`. Spara nyckeln tillsammans med databasbackup; utan den går sparade SNMP-uppgifter inte att dekryptera.
4. Kör `docker compose up --build -d`. Appen lyssnar med okrypterad HTTP på port 8080 på alla nätverksgränssnitt.
5. Peka Cloudflare Tunnel på `http://<serverns IP>:8080` och öppna appen via tunnelns publika värdnamn.

Appen förutsätter att TLS sköts av Cloudflare Tunnel. Sessionscookien kräver HTTPS, så inloggning direkt via `http://<serverns IP>:8080` fungerar inte. Låt cloudflared skicka vidare det publika värdnamnet som `Host` (sätt inte `httpHostHeader`), annars avvisas ändringar med "Ogiltigt ursprung". Lägg gärna Cloudflare Access framför appen.

Begränsa port 8080 i brandväggen så att bara cloudflared-maskinen når den. Docker publicerar portar förbi `ufw` och INPUT-kedjan, så regeln måste ligga i kedjan `DOCKER-USER`, till exempel:

```sh
iptables -I DOCKER-USER -i eth0 -p tcp --dport 8080 -j DROP
iptables -I DOCKER-USER -i eth0 -p tcp --dport 8080 -s <cloudflared-IP> -j ACCEPT
```

Gör reglerna beständiga med till exempel `iptables-persistent`.

Appen behöver åtkomst från servern till enheternas UDP-port 161. Registrera enheterna i webbappen med IP-adress eller DNS-namn. DNS-namn slås upp från appservern vid varje avläsning, så ändrade DNS-poster följs automatiskt. På RouterOS behövs SNMPv3 med kryptering: skapa en community med `security=private`, `authentication-protocol=SHA1` och `encryption-protocol=AES` (RouterOS standard är MD5 och DES), lösenord på minst åtta tecken och `addresses` satt till appserverns IP-adress. Communityns namn är SNMPv3-användaren. Välj samma autentiseringsprotokoll i appen. RouterOS kan även läsas med SNMPv2c, men då skickas community och data okrypterat i nätet, så använd det bara om v3 inte går. På SwOS behöver SNMPv2c vara aktiverat med en unik community. Begränsa SNMP-åtkomsten till appserverns IP-adress i nätet. För automatiska länkförslag måste LLDP vara aktivt på de berörda RouterOS-portarna. SwOS-switchar kopplas ihop automatiskt via MAC-tabellerna.

## Funktion

- Portar och status uppdateras var 15:e sekund. LLDP-information och portmetadata uppdateras ungefär var femte minut.
- Trafikvärden beräknas ur oktetträknare. Appen markerar mätningen som saknad efter omstart, återställda räknare eller när 32-bitarsräknare kan ha slagit runt mer än en gång.
- Detaljerade mätningar sparas i ett dygn. Minutmedelvärden sparas i 30 dagar.
- En länk bekräftas automatiskt när båda sidor rapporterar samma portpar via LLDP. Ensidiga fynd visas som förslag. Förslag kan bekräftas eller nekas; nekade förslag hamnar under Nekade förslag och kan återställas därifrån. En nekning gäller även om förslaget försvinner och dyker upp igen. Enheter utan LLDP kan kopplas manuellt.
- SwOS saknar LLDP. Där skapas länkar ur switcharnas MAC-tabeller (BRIDGE-MIB): om två registrerade enheter ser varandra på var sin port och ingen annan registrerad enhet syns på båda portarna, kopplas portarna ihop. Oregistrerade switchar mellan dem syns inte, så länken går då genom dem. Borttagna länkar återskapas inte.
- Knappen Sortera karta ordnar enheterna som ett träd uppifrån, med routern (eller enheten med flest länkar) överst. Positionerna sparas, även när du flyttar enheter för hand.
- Länkarna färgas efter beläggning i steg om 20 %, från grön (0–20 %) till röd (81–100 %). Beläggningen är den mest belastade riktningen delad med den långsammaste portens hastighet. Grå betyder nere eller saknade mätvärden.
- Moln representerar nät utanför er kontroll, till exempel en uppströmsoperatör eller peering. Skapa ett moln med operatörens namn och eventuellt avtalad kapacitet, och koppla det till routerns port med Ny länk. Trafiken mäts på routerns port; med avtalad kapacitet färgas länken efter avtalet. Moln placeras ovanför routern när kartan sorteras.
- En MAC-upptäckt länk tas bort igen om en registrerad enhet senare visar sig sitta mellan portarna. Manuella länkar lämnas orörda.
- Appen gör inga SNMP-skrivningar och ändrar ingen konfiguration på enheterna.

## Larm

- En enhet som inte svarar på två avläsningar i rad (ungefär 30 sekunder) larmar. Detsamma gäller en bevakad port som är nere. När enheten eller porten är uppe igen skickas ett återställningsmeddelande med hur länge den var nere.
- Alla portar som ingår i en länk på kartan bevakas alltid. Andra portar, till exempel viktiga kundportar, slås på med klockan på portraden i enhetens detaljpanel. Portar på en enhet som inte svarar larmar inte separat; enhetslarmet täcker dem.
- Allt som ändras under samma avläsning skickas som ett meddelande per kanal, så en core-switch som går ner ger inte ett meddelande per port.
- Klockan uppe till höger visar antalet aktiva larm och öppnar larmpanelen. Där ställer du in Discord (webhook-URL från kanalens Integrationer) och e-post (SMTP med STARTTLS, TLS eller okrypterat utan inloggning), och kan skicka ett testmeddelande. Webhook och SMTP-lösenord sparas krypterade med `APP_KEY` och visas aldrig igen i webbläsaren; lämna fälten tomma för att behålla dem.
- Tider i meddelandena visas i tidszonen `TZ` (standard `Europe/Stockholm` i Compose). Återställda larm sparas i 30 dagar.

## Backup och drift

Databasen ligger i Docker-volymen `app_data`. Säkerhetskopiera volymen tillsammans med `APP_KEY`. Administratörskontot skapas vid första start med användarnamnet `admin` och lösenordet i `.env`.

Utveckling: `npm install && npm run build` i `web/`, följt av `go test ./...` och `go run .` i projektroten. Go 1.24 och Node 22 krävs. Vid lokal utveckling behövs samma `APP_KEY`, `ADMIN_PASSWORD` och `DATABASE_PATH` som i Compose.
