# WeatherStar 4000+ (github.com/netbymatt/ws4kp, MIT) with Airwaves Weather
# branding in place of the WeatherStar corner logo. The logo gets its own file
# name so web views that cached the original never show it again.
FROM ghcr.io/netbymatt/ws4kp:latest
COPY airwaves-logo.png /usr/share/nginx/html/images/logos/airwaves-weather.png
COPY airwaves-logo.png /usr/share/nginx/html/images/logos/logo-corner.png
RUN sed -i \
      -e 's|images/logos/logo-corner.png">|images/logos/airwaves-weather.png" width="85" height="67">|g' \
      -e 's|<title>WeatherStar 4000+</title>|<title>Airwaves Weather</title>|' \
      /usr/share/nginx/html/index.html
